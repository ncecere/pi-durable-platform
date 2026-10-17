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
