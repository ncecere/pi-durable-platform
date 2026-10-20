import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import pg from "pg";
import { afterEach, describe, expect, it } from "vitest";
import { AgentOwnedElsewhereError, OWNERSHIP_TABLE, type OpenPostgresAgentStorageOptions, openPostgresAgentStorage } from "../src/lib/storage/postgres.ts";
import { ADMIN_URL, dropSchema, provisionAgents, uniqueSchema, withAdmin, withRole, within } from "./support.ts";

const ctx = BACKGROUND_CONTEXT;
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

const open = (schema: string, connection: pg.ClientConfig = { connectionString: ADMIN_URL }) =>
	openPostgresAgentStorage({ connection, schema, createSchema: true, onConnectionLost: () => {} });

describe("single ownership", () => {
	it("refuses a second owner until the first closes", async () => {
		const schema = uniqueSchema();
		cleanup.push(() => dropSchema(schema));
		const first = await open(schema);
		await expect(open(schema)).rejects.toBeInstanceOf(AgentOwnedElsewhereError);
		await first.storage.close(BACKGROUND_CONTEXT);
		const second = await open(schema);
		await second.storage.close(BACKGROUND_CONTEXT);
	});

	it("fences an owner whose connection was terminated, and lets the next owner in", async () => {
		const schema = uniqueSchema();
		cleanup.push(() => dropSchema(schema));
		const stale = await open(schema);
		cleanup.push(() => stale.storage.close(BACKGROUND_CONTEXT));
		await withAdmin((admin) => admin.query("SELECT pg_terminate_backend($1)", [stale.backendPid]));
		// The stale owner can no longer read or write: its only connection is gone and is never re-established.
		await expect(stale.storage.scanConversations({}, 10, undefined, ctx)).rejects.toThrow();
		const next = await open(schema);
		await next.storage.close(BACKGROUND_CONTEXT);
	});
});

describe("isolation between agents", () => {
	it("an agent's database role cannot read another agent's schema", async () => {
		const a = uniqueSchema("agent_iso");
		const b = uniqueSchema("agent_iso");
		const password = "test-only-password";
		await withAdmin(async (admin) => {
			for (const name of [a, b]) {
				await admin.query(`CREATE ROLE "${name}" LOGIN PASSWORD '${password}'`);
				await admin.query(`CREATE SCHEMA "${name}" AUTHORIZATION "${name}"`);
			}
		});
		cleanup.push(() =>
			withAdmin(async (admin) => {
				for (const name of [a, b]) {
					await admin.query(`DROP SCHEMA IF EXISTS "${name}" CASCADE`);
					await admin.query(`DROP ROLE IF EXISTS "${name}"`);
				}
			}),
		);
		const url = new URL(ADMIN_URL);
		const as = (role: string): pg.ClientConfig => ({
			host: url.hostname,
			port: Number(url.port),
			database: url.pathname.slice(1),
			user: role,
			password,
		});
		// Each agent role creates and uses its own tables.
		const agentA = await openPostgresAgentStorage({ connection: as(a), schema: a });
		await agentA.storage.close(BACKGROUND_CONTEXT);
		const agentB = await openPostgresAgentStorage({ connection: as(b), schema: b });
		await agentB.storage.close(BACKGROUND_CONTEXT);
		// B's role cannot see A's tables.
		const intruder = new pg.Client(as(b));
		await intruder.connect();
		try {
			await expect(intruder.query(`SELECT * FROM "${a}".entries`)).rejects.toThrow(/permission denied/);
		} finally {
			await intruder.end();
		}
	});
});

/** The key a 0.1.x runtime took: `pg_try_advisory_lock(hashtextextended('pdp-agent:<schema>', 0))`. */
const LEGACY_LOCK = "SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS locked";
const legacyName = (schema: string) => `pdp-agent:${schema}`;

/** Open as an agent's own role, as a runtime does (the schema exists; the role cannot create it). */
const openAs = (as: (role: string) => pg.ClientConfig, role: string, schema = role, extra: Partial<OpenPostgresAgentStorageOptions> = {}) =>
	openPostgresAgentStorage({ connection: as(role), schema, onConnectionLost: () => {}, ...extra });

const ownership = (as: (role: string) => pg.ClientConfig, role: string, schema = role) =>
	withRole(as(role), async (client) => {
		const result = await client.query<{ owner_pid: number | null; generation: number }>(
			`SELECT owner_pid, generation::int AS generation FROM "${schema}".${OWNERSHIP_TABLE} WHERE id = 1`,
		);
		return result.rows[0]!;
	});

describe("ownership of one agent's storage", () => {
	it("hands over to a replacement within a bounded time when the owner's connection is lost", async () => {
		const { names, as } = await provisionAgents(1, cleanup);
		const [name] = names as [string];
		const stale = await openAs(as, name);
		cleanup.push(() => stale.storage.close(BACKGROUND_CONTEXT));
		expect((await ownership(as, name)).generation).toBe(1);
		await withAdmin((admin) => admin.query("SELECT pg_terminate_backend($1)", [stale.backendPid]));
		await expect(stale.storage.scanConversations({}, 10, undefined, ctx)).rejects.toThrow();
		// The recorded owner's session is gone: nothing to wait out, but allow the server a moment to finish exiting.
		const started = Date.now();
		const next = await within(5000, () => openAs(as, name));
		cleanup.push(() => next.storage.close(BACKGROUND_CONTEXT));
		expect(Date.now() - started).toBeLessThan(5000);
		expect((await ownership(as, name)).generation).toBe(2);
		// The replacement is the sole owner now.
		await expect(openAs(as, name)).rejects.toBeInstanceOf(AgentOwnedElsewhereError);
	});

	it("hands over at once after a clean close", async () => {
		const { names, as } = await provisionAgents(1, cleanup);
		const [name] = names as [string];
		const first = await openAs(as, name);
		await first.storage.close(BACKGROUND_CONTEXT);
		expect((await ownership(as, name)).owner_pid).toBeNull();
		const second = await openAs(as, name); // no retry: released before the connection ended
		await second.storage.close(BACKGROUND_CONTEXT);
	});

	it("never lets two openers own the agent at once", async () => {
		const schema = uniqueSchema();
		cleanup.push(() => dropSchema(schema));
		await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`)); // `createSchema` itself is not race-free (development only)
		for (let round = 0; round < 3; round++) {
			const results = await Promise.allSettled(Array.from({ length: 8 }, () => open(schema)));
			const owners = results.filter((r) => r.status === "fulfilled");
			expect(owners).toHaveLength(1);
			for (const r of results) {
				if (r.status === "rejected") expect(r.reason).toBeInstanceOf(AgentOwnedElsewhereError);
			}
			await (owners[0] as PromiseFulfilledResult<Awaited<ReturnType<typeof open>>>).value.storage.close(BACKGROUND_CONTEXT);
		}
	});

	it("takes ownership of an agent created before the ownership table, in place", async () => {
		const { names, as } = await provisionAgents(1, cleanup);
		const [name] = names as [string];
		// A 0.1.x agent: its tables, no ownership table.
		const before = await openAs(as, name);
		await before.storage.close(BACKGROUND_CONTEXT);
		await withAdmin((admin) => admin.query(`DROP TABLE "${name}".${OWNERSHIP_TABLE}`));
		const entries = (client: pg.Client) => client.query(`SELECT count(*)::int AS n FROM "${name}".entries`);
		const kept = await withRole(as(name), async (client) => (await entries(client)).rows[0].n);

		// A 0.1.x runtime still running holds the legacy lock (same role): the new runtime stays out.
		const old = new pg.Client(as(name));
		await old.connect();
		try {
			expect((await old.query(LEGACY_LOCK, [legacyName(name)])).rows[0].locked).toBe(true);
			await expect(openAs(as, name)).rejects.toBeInstanceOf(AgentOwnedElsewhereError);
		} finally {
			await old.end();
		}
		// It stopped: the new runtime creates the table and takes over, with the agent's data intact.
		const upgraded = await within(5000, () => openAs(as, name));
		expect((await ownership(as, name)).generation).toBe(1);
		// While it runs, a 0.1.x runtime started now can't take the lock either.
		const late = new pg.Client(as(name));
		await late.connect();
		try {
			expect((await late.query(LEGACY_LOCK, [legacyName(name)])).rows[0].locked).toBe(false);
		} finally {
			await late.end();
		}
		await upgraded.storage.close(BACKGROUND_CONTEXT);
		expect(await withRole(as(name), async (client) => (await entries(client)).rows[0].n)).toBe(kept);
	});

	it("leaves the legacy lock alone when asked", async () => {
		const { names, as } = await provisionAgents(1, cleanup);
		const [name] = names as [string];
		const agent = await openAs(as, name, name, { legacyLock: false });
		cleanup.push(() => agent.storage.close(BACKGROUND_CONTEXT));
		await withAdmin(async (admin) => {
			expect((await admin.query(LEGACY_LOCK, [legacyName(name)])).rows[0].locked).toBe(true);
			await admin.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [legacyName(name)]);
		});
	});
});

describe("another agent's database role cannot take, block, steal or observe-and-grab an agent's ownership", () => {
	it("cannot take ownership of an agent's storage, running or not", async () => {
		const { names, as } = await provisionAgents(2, cleanup);
		const [a, b] = names as [string, string];
		const owner = await openAs(as, a);
		// While A runs: B opening A's schema as itself is refused by the database, not by the lock.
		await expect(openAs(as, b, a)).rejects.toThrow(/permission denied/);
		await owner.storage.close(BACKGROUND_CONTEXT);
		// While A is hibernated (free): the same.
		const attempt = await openAs(as, b, a).then(
			(taken) => taken.storage.close(BACKGROUND_CONTEXT).then(() => "took it"),
			(error: Error) => error.message,
		);
		expect(attempt).toMatch(/permission denied/);
		// And A starts normally afterwards: nothing of B's is in the way.
		const again = await openAs(as, a);
		await again.storage.close(BACKGROUND_CONTEXT);
	});

	it("cannot write, lock, drop or read A's ownership record, or end A's session", async () => {
		const { names, as } = await provisionAgents(2, cleanup);
		const [a, b] = names as [string, string];
		const owner = await openAs(as, a);
		cleanup.push(() => owner.storage.close(BACKGROUND_CONTEXT));
		const table = `"${a}".${OWNERSHIP_TABLE}`;
		await withRole(as(b), async (intruder) => {
			for (const sql of [
				`SELECT * FROM ${table}`,
				`UPDATE ${table} SET owner_pid = NULL, owner_started = NULL`,
				`INSERT INTO ${table} (id) VALUES (2)`,
				`DELETE FROM ${table}`,
				`TRUNCATE ${table}`,
				`SELECT * FROM ${table} FOR UPDATE`,
				`DROP TABLE ${table}`,
				`ALTER TABLE ${table} DISABLE ROW LEVEL SECURITY`,
				`CREATE TABLE "${a}".planted (x int)`,
				`CREATE SCHEMA "${a}_shadow" AUTHORIZATION "${a}"`,
			]) {
				await expect(intruder.query(sql), sql).rejects.toThrow(/permission denied|must be (owner|member)/);
			}
			// Nor hold a lock on it, which would stall A's next start.
			await intruder.query("BEGIN");
			await expect(intruder.query(`LOCK TABLE ${table} IN ACCESS EXCLUSIVE MODE`)).rejects.toThrow(/permission denied/);
			await intruder.query("ROLLBACK");
			// Nor end A's session, so A's restart can't be provoked or A's ownership released.
			await expect(intruder.query("SELECT pg_terminate_backend($1)", [owner.backendPid])).rejects.toThrow(/permission denied/);
			await expect(intruder.query("SELECT pg_cancel_backend($1)", [owner.backendPid])).rejects.toThrow(/permission denied/);
		});
		// A is still the owner and still works.
		expect((await ownership(as, a)).owner_pid).toBe(owner.backendPid);
		await expect(owner.storage.scanConversations({}, 10, undefined, ctx)).resolves.toBeDefined();
	});

	it("cannot block A's start by holding A's legacy lock key, before, during or after A runs", async () => {
		const { names, as } = await provisionAgents(2, cleanup);
		const [a, b] = names as [string, string];
		const intruder = new pg.Client(as(b));
		await intruder.connect();
		cleanup.push(() => intruder.end());
		const key = legacyName(a);

		// Before: B takes A's key while A is hibernated, and keeps it.
		expect((await intruder.query(LEGACY_LOCK, [key])).rows[0].locked).toBe(true);
		const first = await within(5000, () => openAs(as, a));
		expect((await ownership(as, a)).owner_pid).toBe(first.backendPid);
		// During: with B still holding the key, a second process of A is refused by the ownership record.
		await expect(openAs(as, a)).rejects.toBeInstanceOf(AgentOwnedElsewhereError);

		// After (observe and grab): B watches pg_locks for A's lock key to appear and disappear. A never held the key
		// here (B did), so release it, let A run and stop, then grab the key the moment it is free.
		await intruder.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [key]);
		await first.storage.close(BACKGROUND_CONTEXT);
		const second = await within(5000, () => openAs(as, a));
		const held = await intruder.query(
			"SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted AND pid = $1",
			[second.backendPid],
		);
		expect(held.rowCount).toBe(1); // B can see that A runs, and under which key...
		await withAdmin((admin) => admin.query("SELECT pg_terminate_backend($1)", [second.backendPid]));
		await expect(second.storage.scanConversations({}, 10, undefined, ctx)).rejects.toThrow();
		await within(5000, async () => {
			if ((await intruder.query(LEGACY_LOCK, [key])).rows[0].locked !== true) throw new Error("A's key not free yet");
		});
		// ...and grabs the key as soon as A's session is gone. A's replacement starts all the same, and stays the only owner.
		const third = await within(5000, () => openAs(as, a));
		cleanup.push(() => third.storage.close(BACKGROUND_CONTEXT));
		expect((await ownership(as, a)).owner_pid).toBe(third.backendPid);
		await expect(openAs(as, a)).rejects.toBeInstanceOf(AgentOwnedElsewhereError);
		// B still holds A's key. With the compatibility lock off the key doesn't matter at all: a second opener is
		// refused by the ownership record alone.
		await expect(openAs(as, a, a, { legacyLock: false })).rejects.toBeInstanceOf(AgentOwnedElsewhereError);
	});

	it("cannot make A run beside another owner by holding A's key", async () => {
		const { names, as } = await provisionAgents(2, cleanup);
		const [a, b] = names as [string, string];
		const intruder = new pg.Client(as(b));
		await intruder.connect();
		cleanup.push(() => intruder.end());
		const running = await openAs(as, a);
		cleanup.push(() => running.storage.close(BACKGROUND_CONTEXT));
		// B can't get A's key (A holds it) and A can't be made to give it up.
		expect((await intruder.query(LEGACY_LOCK, [legacyName(a)])).rows[0].locked).toBe(false);
		expect((await ownership(as, a)).owner_pid).toBe(running.backendPid);
	});
});
