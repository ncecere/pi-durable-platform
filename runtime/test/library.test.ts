import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import * as durable from "@earendil-works/pi-durable";
import {
	AgentDefinitionError,
	assistantAgent,
	CONTRACT_VERSION,
	defineAgent,
	defineDoc,
	defineExtension,
	defineTool,
	GenerationTask,
	hook,
	PI_DURABLE_VERSION,
	section,
	ToolTask,
	Type,
} from "@pdp/runtime";
import { fakeModel, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const ctx = BACKGROUND_CONTEXT;
const running: TestAgent[] = [];
afterEach(async () => {
	while (running.length > 0) await running.pop()!.close();
});
const start = async (...args: Parameters<typeof startTestAgent>) => {
	const agent = await startTestAgent(...args);
	running.push(agent);
	return agent;
};

const tool = (name: string) => defineTool({ name, description: name, parameters: Type.Object({}), execute: async () => ({ output: [] }) });

describe("defineAgent", () => {
	it.each([
		["a reserved extension name", { extensions: [defineExtension({ name: "mcp" })] }, /reserved/],
		["an extension named like a thread's MCP servers", { extensions: [defineExtension({ name: "mcp-thread:t1" })] }, /reserved/],
		["a pdp extension name", { extensions: [defineExtension({ name: "pdp.audit" })] }, /reserved/],
		["tool search's name", { extensions: [defineExtension({ name: "tool-search" })] }, /reserved/],
		["two extensions with one name (one would replace the other)", { extensions: [defineExtension({ name: "a" }), defineExtension({ name: "a" })] }, /two extensions/],
		["a tool in the MCP namespace", { extensions: [defineExtension({ name: "a", tools: [tool("mcp__github__x")] })] }, /reserved for MCP/],
		["a tool in the app MCP namespace", { extensions: [defineExtension({ name: "a", tools: [tool("mcpc__x__y")] })] }, /reserved for MCP/],
		["a pi. task", { extensions: [{ name: "a", tasks: [{ definition: { name: "pi.mine" } }] } as never] }, /task name/],
		["a pi. document", { documents: [defineDoc({ kind: "pi.notes", version: 1, scope: "session", initial: () => ({}) })] }, /reserved/],
		["a pdp. document", { documents: [defineDoc({ kind: "pdp.threads", version: 1, scope: "session", initial: () => ({}) })] }, /reserved/],
		["a task document", { documents: [defineDoc({ kind: "acme.t", version: 1, scope: "task", initial: () => ({}) }) as never] }, /only session and conversation/],
		["an extension selection in settings", { settings: { extensions: [] } as never }, /library's/],
		["an empty kind in waitsForPerson", { waitsForPerson: [""] }, /waitsForPerson/],
		["waitsForPerson that is not a list", { waitsForPerson: "acme.ask" as never }, /waitsForPerson/],
	])("refuses %s", (_what, definition, message) => {
		expect(() => defineAgent(definition as never)).toThrow(AgentDefinitionError);
		expect(() => defineAgent(definition as never)).toThrow(message);
	});

	it("checks extensions given as a function when the agent starts", async () => {
		const definition = defineAgent({ extensions: () => [defineExtension({ name: "mcp" })] });
		await expect(startTestAgent(definition)).rejects.toThrow(AgentDefinitionError);
	});
});

describe("the facade", () => {
	it("re-exports pi-durable's authoring API unchanged", () => {
		expect(defineTool).toBe(durable.defineTool);
		expect(defineExtension).toBe(durable.defineExtension);
		expect(defineDoc).toBe(durable.defineDoc);
		expect(section).toBe(durable.section);
		expect(hook).toBe(durable.hook);
		expect(ToolTask).toBe(durable.ToolTask);
		expect(GenerationTask).toBe(durable.GenerationTask);
		expect(typeof Type.Object).toBe("function");
	});
});

describe("versions", () => {
	it("reports the pinned pi-durable version and the contract version", async () => {
		const require = createRequire(import.meta.url);
		const installed = JSON.parse(readFileSync(require.resolve("@earendil-works/pi-durable").replace(/dist\/index\.js$/, "package.json"), "utf8")) as { version: string };
		const ours = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { dependencies: Record<string, string> };
		expect(PI_DURABLE_VERSION).toBe(installed.version);
		expect(PI_DURABLE_VERSION).toBe(ours.dependencies["@earendil-works/pi-durable"]);
		expect(CONTRACT_VERSION).toBe(2);
		const agent = await start();
		expect(await agent.status()).toMatchObject({ contractVersion: 2, piDurableVersion: PI_DURABLE_VERSION });
	});
});

describe("the default composition", () => {
	it("is the assistant: the template's instructions and get_time", async () => {
		const model = fakeModel();
		let system = "";
		model.reply((context) => {
			system = JSON.stringify(context.messages.filter((m) => (m.role as string) === "system"));
			return toolCall("get_time", { timeZone: "UTC" }, { id: "t1" });
		}, "It is now.");
		const agent = await start(assistantAgent, { model, instructions: "Be terse." });
		await agent.send("a", "what time is it?");
		const view = await agent.settled("a");
		const result = view.messages.find((m) => m.role === "tool");
		expect(result).toMatchObject({ role: "tool", name: "get_time", isError: false });
		expect(result?.role === "tool" && result.text).toMatch(/\(UTC\)$/);
		const conversation = (await agent.agent.threads.get("a"))!;
		expect((await agent.harness.snapshot(durable.AgentDoc, conversation.id, ctx))?.extensions).toEqual(["assistant"]);
		expect(system).toContain("Be terse.");
	});
});

describe("documents, conversationCreated and settings", () => {
	const Notes = defineDoc<{ items: string[] }>({ kind: "acme.notes", version: 1, scope: "conversation", history: "latest", fork: "initial", initial: () => ({ items: [] }) });
	const Counter = defineDoc<{ starts: number }>({ kind: "acme.counter", version: 1, scope: "session", initial: () => ({ starts: 0 }) });

	it("creates declared documents in every conversation, runs conversationCreated, and keeps settings live", async () => {
		const created: number[] = [];
		let reads = 0;
		const definition = defineAgent({
			documents: [Notes, Counter],
			conversationCreated: async (tx, conversation) => {
				created.push(conversation.id);
				// Runs after the declared documents exist.
				(await tx.doc(Notes, conversation.id)).items.push("created");
			},
			settings: {
				get followUpMode() {
					reads++;
					return "one-at-a-time" as const;
				},
			},
		});
		const agent = await start(definition);
		expect(await agent.harness.snapshot(Counter, ctx)).toEqual({ starts: 0 });
		await agent.send("one", "hi");
		await agent.send("two", "hi");
		await agent.settled("one");
		await agent.settled("two");
		for (const thread of ["one", "two"]) {
			const conversation = (await agent.agent.threads.get(thread))!;
			expect(created).toContain(conversation.id);
			expect(await agent.harness.snapshot(Notes, conversation.id, ctx)).toEqual({ items: ["created"] });
		}
		// A getter is read where pi-durable uses the setting, not copied once.
		expect(reads).toBeGreaterThan(0);
	});
});
