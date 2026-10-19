import { randomBytes } from "node:crypto";
import pg from "pg";

/** Admin connection for tests: `PDP_TEST_DATABASE_URL`, default the local throwaway container. */
export const ADMIN_URL = process.env.PDP_TEST_DATABASE_URL ?? "postgres://pdp:pdp-local-only@127.0.0.1:55432/pdp";

export function uniqueSchema(prefix = "agent_t"): string {
	return `${prefix}_${randomBytes(6).toString("hex")}`;
}

export async function withAdmin<T>(use: (client: pg.Client) => Promise<T>): Promise<T> {
	const client = new pg.Client({ connectionString: ADMIN_URL });
	await client.connect();
	try {
		return await use(client);
	} finally {
		await client.end();
	}
}

export async function dropSchema(schema: string): Promise<void> {
	await withAdmin((admin) => admin.query(`DROP SCHEMA IF EXISTS "${schema}" CASCADE`));
}

export const AGENT_PASSWORD = "test-only-password";

/**
 * Agents as the platform provisions them (internal/agenthost/provision): a login role that owns its schema, with the
 * same attributes and a connection limit of 2. Returns the role names (schema = role) and a connection config for each;
 * the cleanup drops them.
 */
export async function provisionAgents(
	count: number,
	cleanup: (() => Promise<unknown>)[],
): Promise<{ names: string[]; as: (role: string) => pg.ClientConfig }> {
	const names = Array.from({ length: count }, () => uniqueSchema("agent_own"));
	await withAdmin(async (admin) => {
		for (const name of names) {
			await admin.query(`CREATE ROLE "${name}" LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT 2 PASSWORD '${AGENT_PASSWORD}'`);
			await admin.query(`CREATE SCHEMA "${name}" AUTHORIZATION "${name}"`);
		}
	});
	cleanup.push(() =>
		withAdmin(async (admin) => {
			for (const name of names) {
				await admin.query("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", [name]);
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
		password: AGENT_PASSWORD,
	});
	return { names, as };
}

/** A plain connection as a role, closed after `use`. */
export async function withRole<T>(config: pg.ClientConfig, use: (client: pg.Client) => Promise<T>): Promise<T> {
	const client = new pg.Client(config);
	client.on("error", () => {});
	await client.connect();
	try {
		return await use(client);
	} finally {
		await client.end().catch(() => {});
	}
}

/** Retry `attempt` until it succeeds or `ms` pass: for what must happen within a bounded time. */
export async function within<T>(ms: number, attempt: () => Promise<T>): Promise<T> {
	const deadline = Date.now() + ms;
	for (;;) {
		try {
			return await attempt();
		} catch (error) {
			if (Date.now() >= deadline) throw error;
			await new Promise((resolve) => setTimeout(resolve, 50));
		}
	}
}
