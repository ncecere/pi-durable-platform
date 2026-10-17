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

/** Another process holds this agent's ownership lock. Nothing was written. */
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

	constructor(client: pg.Client) {
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
};

export type PostgresAgentStorage = {
	readonly storage: SqliteStorage;
	/** Backend PID of the owning connection (lets an operator evict a stuck owner with `pg_terminate_backend`). */
	readonly backendPid: number;
};

/**
 * Open one agent's durable storage in its own PostgreSQL schema and take its ownership lock.
 *
 * Ownership is a session-level advisory lock held by this connection for its whole life. A second opener fails with
 * `AgentOwnedElsewhereError`. Because the connection is never re-established, a process that loses its connection
 * also loses the ability to write, and the lock is released for the next owner: the lock and the write path share one
 * fate, which is what keeps a stale pod from writing after its replacement took over.
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
		const quoted = `"${options.schema}"`;
		if (options.createSchema) await client.query(`CREATE SCHEMA IF NOT EXISTS ${quoted}`);
		const exists = await client.query("SELECT 1 FROM pg_namespace WHERE nspname = $1", [options.schema]);
		if (exists.rowCount !== 1) throw new Error(`Agent schema ${options.schema} does not exist`);
		await client.query(`SET search_path TO ${quoted}`);
		const locked = await client.query<{ locked: boolean }>(
			"SELECT pg_try_advisory_lock(hashtextextended($1, 0)) AS locked",
			[`pdp-agent:${options.schema}`],
		);
		if (locked.rows[0]?.locked !== true) throw new AgentOwnedElsewhereError(options.schema);
		const pid = await client.query<{ pid: number }>("SELECT pg_backend_pid() AS pid");
		const database = new PostgresSqliteDatabase(client);
		const storage = await SqliteStorage.open(database);
		return { storage, backendPid: pid.rows[0]!.pid };
	} catch (error) {
		await client.end().catch(() => {});
		throw error;
	}
}
