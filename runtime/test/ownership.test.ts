import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import pg from "pg";
import { afterEach, describe, expect, it } from "vitest";
import { AgentOwnedElsewhereError, openPostgresAgentStorage } from "../src/storage/postgres.ts";
import { ADMIN_URL, dropSchema, uniqueSchema, withAdmin } from "./support.ts";

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
