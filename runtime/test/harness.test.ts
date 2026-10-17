import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { createModels } from "@earendil-works/pi-ai/models";
import { fauxAssistantMessage, fauxProvider, fauxText } from "@earendil-works/pi-ai/providers/faux";
import { createRegistry, Harness } from "@earendil-works/pi-durable";
import { afterEach, describe, expect, it } from "vitest";
import { openPostgresAgentStorage } from "../src/storage/postgres.ts";
import { ADMIN_URL, dropSchema, uniqueSchema } from "./support.ts";

const ctx = BACKGROUND_CONTEXT;
let schema: string | undefined;
afterEach(async () => {
	if (schema !== undefined) await dropSchema(schema);
});

/** A scripted model that reports how many user messages it was given. */
function countingModels() {
	const seen: number[] = [];
	const faux = fauxProvider({ provider: "test", models: [{ id: "count", name: "count" }], tokensPerSecond: 0 });
	const respond = (context: { messages: readonly { role: string }[] }) => {
		faux.appendResponses([respond]);
		const users = context.messages.filter((message) => message.role === "user").length;
		seen.push(users);
		return fauxAssistantMessage([fauxText(`user messages so far: ${users}`)]);
	};
	faux.setResponses([respond]);
	const models = createModels();
	models.setProvider(faux.provider);
	return { models, seen, model: { provider: "test", modelId: "count" } };
}

describe("Pi Durable harness on PostgreSQL", () => {
	it("keeps the conversation across a process restart", async () => {
		schema = uniqueSchema("agent_h");
		const open = async () => {
			const { storage } = await openPostgresAgentStorage({ connection: { connectionString: ADMIN_URL }, schema: schema!, createSchema: true });
			const m = countingModels();
			const harness = await Harness.open(storage, { models: m.models, registry: createRegistry() }, ctx);
			return { harness, m };
		};

		const first = await open();
		const root1 = await first.harness.root(ctx, { agent: { model: first.m.model } });
		const s1 = await root1.submit({ type: "input", content: "hello", requestId: "m1" }, ctx);
		expect((await s1.wait(ctx)).status).toBe("done");
		await first.harness.close(ctx);

		// A new process: fresh storage connection and harness, same schema.
		const second = await open();
		const root2 = await second.harness.root(ctx);
		expect(root2.id).toBe(root1.id);
		// Retrying the first request is deduplicated, not submitted twice.
		const again = await root2.submit({ type: "input", content: "hello", requestId: "m1" }, ctx);
		expect(again.id).toBe(s1.id);
		const s2 = await root2.submit({ type: "input", content: "still there?", requestId: "m2" }, ctx);
		expect((await s2.wait(ctx)).status).toBe("done");
		expect(second.m.seen).toEqual([2]);
		await second.harness.close(ctx);
	});
});
