import pg from "pg";
import { SqliteStorage } from "@earendil-works/pi-durable/storage/sqlite";
import { translateSql } from "./translate.ts";

/** Values `SqliteStorage` binds (mirrors pi-durable's internal `SqliteValue`). */
type SqlValue = null | number | bigint | string | Uint8Array;

interface Executor {
	exec(sql: string): Promise<void>;
	run(sql: string, ...params: SqlValue[]): Promise<void>;
	get<T extends object>(sql: string, ...params: SqlValue[]): Promise<T | undefined>;
	all<T extends object>(sql: string, ...params: SqlValue[]): Promise<T[]>;
}

/** Another process owns this agent's storage. Nothing was written. */
export class AgentOwnedElsewhereError extends Error {
	constructor(schema: string) {
		super(`Agent storage ${schema} is owned by another process`);
		this.name = "AgentOwnedElsewhereError";
	}
}

const SCHEMA_PATTERN = /^[a-z_][a-z0-9_]{0,62}$/;

export function assertSchemaName(schema: string): void {
	if (!SCHEMA_PATTERN.test(schema)) throw new Error(`Invalid agent schema name ${JSON.stringify(schema)}`);
}

/** int8 (OID 20) as a JavaScript number: Pi's IDs and sequences stay within the safe integer range. */
const types = {
	getTypeParser(oid: number, format?: "text" | "binary") {
		if (oid === 20) {
			return (value: string) => {
				const n = Number(value);
				if (!Number.isSafeInteger(n)) throw new RangeError(`int8 value ${value} exceeds the safe integer range`);
				return n;
			};
		}
		return pg.types.getTypeParser(oid, format as "text");
	},
};

const bind = (params: readonly SqlValue[]): unknown[] =>
	params.map((value) => {
		if (value instanceof Uint8Array) return Buffer.from(value.buffer, value.byteOffset, value.byteLength);
		if (typeof value === "bigint") return value.toString();
		return value;
	});

/** Runs operations strictly one at a time, in call order. */
class SerialQueue {
	private tail: Promise<unknown> = Promise.resolve();

	run<T>(operation: () => Promise<T>): Promise<T> {
		const result = this.tail.then(operation, operation);
		this.tail = result.catch(() => {});
		return result;
	}
}

class PgExecutor implements Executor {
	constructor(
		protected readonly client: pg.Client,
		private readonly check: () => void = () => {},
	) {}

	async exec(sql: string): Promise<void> {
		this.check();
		await this.client.query(translateSql(sql));
	}

	async run(sql: string, ...params: SqlValue[]): Promise<void> {
		this.check();
		await this.client.query(translateSql(sql), bind(params));
	}

	async get<T extends object>(sql: string, ...params: SqlValue[]): Promise<T | undefined> {
		this.check();
		const result = await this.client.query(translateSql(sql), bind(params));
		return result.rows[0] as T | undefined;
	}

	async all<T extends object>(sql: string, ...params: SqlValue[]): Promise<T[]> {
		this.check();
		const result = await this.client.query(translateSql(sql), bind(params));
		return result.rows as T[];
	}
}

/**
 * `SqliteDatabase` facade over one dedicated PostgreSQL connection, so pi-durable's `SqliteStorage` runs unchanged.
 * Every operation is serialized; a transaction holds the queue until it commits or rolls back, as the facade contract
 * requires. The connection is never re-established: losing it fails the storage, and Pi fails the Session.
 */
export class PostgresSqliteDatabase extends PgExecutor {
	private readonly queue = new SerialQueue();
	private closed = false;

	/** `beforeClose` runs once, best effort, before the connection ends (it releases ownership for a prompt hand-over). */
	constructor(
		client: pg.Client,
		private readonly beforeClose: () => Promise<void> = async () => {},
	) {
		super(client);
	}

	override exec(sql: string): Promise<void> {
		return this.queue.run(() => super.exec(sql));
	}

	override run(sql: string, ...params: SqlValue[]): Promise<void> {
		return this.queue.run(() => super.run(sql, ...params));
	}

	override get<T extends object>(sql: string, ...params: SqlValue[]): Promise<T | undefined> {
		return this.queue.run(() => super.get<T>(sql, ...params));
	}

	override all<T extends object>(sql: string, ...params: SqlValue[]): Promise<T[]> {
		return this.queue.run(() => super.all<T>(sql, ...params));
	}

	transaction<T>(callback: (transaction: Executor) => Promise<T>): Promise<T> {
		return this.queue.run(async () => {
			await this.client.query("BEGIN");
			let active = true;
			const transaction = new PgExecutor(this.client, () => {
				if (!active) throw new Error("PostgreSQL transaction handle is no longer active");
			});
			try {
				const result = await callback(transaction);
				await this.client.query("COMMIT");
				return result;
			} catch (error) {
				try {
					await this.client.query("ROLLBACK");
				} catch (rollbackError) {
					throw new AggregateError([error, rollbackError], "PostgreSQL rollback failed");
				}
				throw error;
			} finally {
				active = false;
			}
		});
	}

	close(): Promise<void> {
		return this.queue.run(async () => {
			if (this.closed) return;
			this.closed = true;
			await this.beforeClose().catch(() => {});
			await this.client.end();
		});
	}
}

export type OpenPostgresAgentStorageOptions = {
	/** Connection settings for this agent's database role. */
	connection: pg.ClientConfig;
	/** The agent's schema. Its tables live only there. */
	schema: string;
	/** Create the schema if missing. The platform normally provisions it and the agent role cannot. */
	createSchema?: boolean;
	/** Called once if the connection drops; the storage is unusable afterwards. */
	onConnectionLost?: (error: Error) => void;
	/**
	 * Also take the session advisory lock that runtimes before the ownership table (platform 0.1.x) used, so such a
	 * runtime and this one can't own the agent together during a rolling upgrade (default true). Only a holder running
	 * as this agent's own database role is honoured: the lock is database-wide, so any other role can take the key, and
	 * that must not keep this agent from starting. Turn it off once no 0.1.x runtime is left.
	 */
	legacyLock?: boolean;
};

export type PostgresAgentStorage = {
	readonly storage: SqliteStorage;
	/** Backend PID of the owning connection (lets an operator evict a stuck owner with `pg_terminate_backend`). */
	readonly backendPid: number;
};

/** Where an agent's ownership is recorded: a one-row table in the agent's own schema, writable only by its role. */
export const OWNERSHIP_TABLE = "pdp_ownership";

/** The session advisory lock key of runtimes before the ownership table (still taken for rolling upgrades). */
const legacyLockKey = (schema: string): string => `pdp-agent:${schema}`;

const quoteIdent = (name: string): string => `"${name}"`;

/** PostgreSQL error codes of two sessions racing to create the same table. */
const CREATE_RACE = new Set(["23505", "42P07", "42710"]);

async function ensureOwnershipTable(client: pg.Client, table: string): Promise<void> {
	for (let attempt = 0; ; attempt++) {
		try {
			await client.query(
				`CREATE TABLE IF NOT EXISTS ${table} (
					id integer PRIMARY KEY CHECK (id = 1),
					owner_pid integer,
					owner_started timestamptz,
					taken_at timestamptz,
					generation bigint NOT NULL DEFAULT 0
				)`,
			);
			return;
		} catch (error) {
			// Two processes of the same agent starting together (an upgrade of an agent that has no table yet).
			if (attempt < 3 && CREATE_RACE.has((error as { code?: string }).code ?? "")) continue;
			throw error;
		}
	}
}

/**
 * Take the legacy advisory lock if it is free. Returns whether this connection now holds it. Throws
 * `AgentOwnedElsewhereError` when a session of this same role holds it (a 0.1.x runtime of this agent). A holder
 * running as another role is ignored: it can only be another agent probing, and it is not an owner.
 */
async function takeLegacyLock(client: pg.Client, schema: string): Promise<boolean> {
	const key = "hashtextextended($1, 0)";
	const locked = await client.query<{ locked: boolean }>(`SELECT pg_try_advisory_lock(${key}) AS locked`, [legacyLockKey(schema)]);
	if (locked.rows[0]?.locked === true) return true;
	const holder = await client.query(
		`SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
		 WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
		   AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
		   AND l.classid = ((${key} >> 32) & 4294967295)::bigint::oid
		   AND l.objid = (${key} & 4294967295)::bigint::oid
		   AND a.usename = session_user`,
		[legacyLockKey(schema)],
	);
	if ((holder.rowCount ?? 0) > 0) throw new AgentOwnedElsewhereError(schema);
	console.warn(`[storage ${schema}] another database role holds this agent's legacy lock key; ignored (ownership is recorded in ${OWNERSHIP_TABLE})`);
	return false;
}

/**
 * Take ownership: record this session as the owner in the ownership table, unless the recorded owner's session still
 * exists. The row is locked while deciding, so two openers are serialized and the second sees the first. A session is
 * identified by backend pid and start time (a recycled pid has another start time), as listed in `pg_stat_activity`,
 * where an agent role sees its own role's sessions. Throws `AgentOwnedElsewhereError` when the owner is alive.
 *
 * Everything the decision rests on is in the agent's own schema or is the agent's own sessions, so no other role
 * can take, hold or forge it. `pg_stat_activity` is read only after the row lock is held: its snapshot lasts the
 * whole transaction, and one taken before waiting for another opener could miss that opener's new session.
 */
async function takeOwnership(client: pg.Client, schema: string, table: string): Promise<void> {
	await client.query("BEGIN");
	try {
		await client.query("SET LOCAL lock_timeout = '5s'");
		await client.query(`INSERT INTO ${table} (id) VALUES (1) ON CONFLICT (id) DO NOTHING`);
		await client.query(`SELECT 1 FROM ${table} WHERE id = 1 FOR UPDATE`);
		const self = await client.query<{ started: Date | null }>(
			"SELECT backend_start AS started FROM pg_stat_activity WHERE pid = pg_backend_pid()",
		);
		if (self.rows[0]?.started == null) {
			// Without this, a live owner could look dead and be taken over.
			throw new Error("This session is not visible in pg_stat_activity, so agent ownership can't be decided (track_activities off?)");
		}
		const alive = await client.query<{ alive: boolean }>(
			`SELECT EXISTS (SELECT 1 FROM pg_stat_activity a, ${table} o
			 WHERE o.id = 1 AND a.pid = o.owner_pid AND a.backend_start = o.owner_started AND a.usename = session_user) AS alive`,
		);
		if (alive.rows[0]?.alive === true) throw new AgentOwnedElsewhereError(schema);
		await client.query(
			`UPDATE ${table} SET owner_pid = pg_backend_pid(),
			   owner_started = (SELECT backend_start FROM pg_stat_activity WHERE pid = pg_backend_pid()),
			   taken_at = now(), generation = generation + 1 WHERE id = 1`,
		);
		await client.query("COMMIT");
	} catch (error) {
		await client.query("ROLLBACK").catch(() => {});
		// Another opener holds the row for longer than a decision takes: it is taking ownership right now.
		if ((error as { code?: string }).code === "55P03") throw new AgentOwnedElsewhereError(schema);
		throw error;
	}
}

/**
 * Open one agent's durable storage in its own PostgreSQL schema and take its ownership.
 *
 * Ownership is a row in the agent's own schema (`pdp_ownership`) naming the owning database session. A second opener
 * fails with `AgentOwnedElsewhere` while that session exists; when it is gone (the process exited, the connection
 * dropped, the backend was terminated) the next opener takes over at once, with no lease to wait out. Because the
 * connection is never re-established, a process that loses its connection also loses the ability to write, and its
 * session ends for the next owner: the ownership and the write path share one fate, which is what keeps a stale pod
 * from writing after its replacement took over. No other agent's role can write the table, lock it, or end the
 * owner's session, which the database-wide advisory lock this replaced could not promise.
 *
 * An agent created by platform 0.1.x has no ownership table: it is created here, in place, on first start. Until every
 * runtime is upgraded the legacy advisory lock is also taken (see `legacyLock`).
 */
export async function openPostgresAgentStorage(options: OpenPostgresAgentStorageOptions): Promise<PostgresAgentStorage> {
	assertSchemaName(options.schema);
	const client = new pg.Client({
		keepAlive: true,
		keepAliveInitialDelayMillis: 10_000,
		...options.connection,
		types,
		application_name: `pdp-agent:${options.schema}`,
	});
	let lost = false;
	client.on("error", (error) => {
		if (lost) return;
		lost = true;
		options.onConnectionLost?.(error);
	});
	await client.connect();
	try {
		// The server drops a dead client's session (and its lock) within about a minute.
		await client.query("SET tcp_keepalives_idle = 30");
		await client.query("SET tcp_keepalives_interval = 10");
		await client.query("SET tcp_keepalives_count = 3");
		await client.query("SET idle_in_transaction_session_timeout = '60s'");
		const quoted = quoteIdent(options.schema);
		if (options.createSchema) await client.query(`CREATE SCHEMA IF NOT EXISTS ${quoted}`);
		const exists = await client.query("SELECT 1 FROM pg_namespace WHERE nspname = $1", [options.schema]);
		if (exists.rowCount !== 1) throw new Error(`Agent schema ${options.schema} does not exist`);
		await client.query(`SET search_path TO ${quoted}`);
		const table = `${quoted}.${OWNERSHIP_TABLE}`;
		await ensureOwnershipTable(client, table);
		const legacy = options.legacyLock === false ? false : await takeLegacyLock(client, options.schema);
		await takeOwnership(client, options.schema, table);
		const pid = await client.query<{ pid: number }>("SELECT pg_backend_pid() AS pid");
		const database = new PostgresSqliteDatabase(client, async () => {
			// A clean stop hands over at once: the next owner doesn't wait for this session to disappear.
			await client.query(`UPDATE ${table} SET owner_pid = NULL, owner_started = NULL WHERE id = 1 AND owner_pid = pg_backend_pid()`);
			if (legacy) await client.query("SELECT pg_advisory_unlock(hashtextextended($1, 0))", [legacyLockKey(options.schema)]);
		});
		const storage = await SqliteStorage.open(database);
		return { storage, backendPid: pid.rows[0]!.pid };
	} catch (error) {
		await client.end().catch(() => {});
		throw error;
	}
}
