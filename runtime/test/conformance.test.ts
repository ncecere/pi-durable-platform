import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { registerStorageConformance } from "@earendil-works/pi-durable/testing";
import { describe, expect, it } from "vitest";
import { openPostgresAgentStorage } from "../src/lib/storage/postgres.ts";
import { ADMIN_URL, dropSchema, uniqueSchema } from "./support.ts";

// pi-durable's own storage contract, run against PostgreSQL. Each case gets a fresh agent schema.
registerStorageConformance({ describe, expect, it }, "PostgreSQL agent storage", async (use) => {
	const schema = uniqueSchema("agent_conf");
	const { storage } = await openPostgresAgentStorage({ connection: { connectionString: ADMIN_URL }, schema, createSchema: true });
	try {
		await use(storage);
	} finally {
		await storage.close(BACKGROUND_CONTEXT);
		await dropSchema(schema);
	}
});
