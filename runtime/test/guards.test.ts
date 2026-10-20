import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentDoc } from "@earendil-works/pi-durable";
import { assistantAgent, assistantExtension, defineAgent, defineExtension, GenerationTask, hook, ToolTask } from "@pdp/runtime";
import { afterGate, fakeModel, gate, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const ctx = BACKGROUND_CONTEXT;
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});
const track = (agent: TestAgent) => {
	cleanup.push(() => agent.close());
	return agent;
};

/** A guard extension: it blocks get_time and counts the model requests it sees. */
function guarded() {
	const requests: number[] = [];
	const guard = defineExtension({
		name: "guard",
		hooks: [
			hook(GenerationTask, {
				beforeRequest: (_request, api) => {
					requests.push(api.conversationId);
					return undefined;
				},
			}),
			hook(ToolTask, { beforeTool: (call) => (call.name === "get_time" ? { block: "get_time is blocked by the guard" } : undefined) }),
		],
	});
	const definition = defineAgent({ extensions: ({ instructions }) => [assistantExtension(instructions), guard] });
	return { definition, requests };
}

/** The tool result of call `id` in a thread. */
async function resultOf(agent: TestAgent, threadId: string, id: string) {
	const view = await agent.settled(threadId);
	const result = view.messages.find((m) => m.role === "tool" && m.toolCallId === id);
	if (result?.role !== "tool") throw new Error(`no result for ${id}`);
	return result;
}

async function selection(agent: TestAgent, threadId: string): Promise<unknown> {
	const conversation = (await agent.agent.threads.get(threadId))!;
	return (await agent.harness.snapshot(AgentDoc, conversation.id, ctx))?.extensions;
}

async function server(handler: (res: import("node:http").ServerResponse) => void): Promise<string> {
	const s: Server = createServer((req, res) => {
		req.resume();
		handler(res);
	});
	await new Promise<void>((resolve) => s.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => s.close(resolve)));
	return `http://127.0.0.1:${(s.address() as AddressInfo).port}`;
}

const blocked = (result: { text: string; isError: boolean }) => result.isError && result.text.includes("get_time is blocked by the guard");

// The runtime reapplies a thread's extension selection on every message with an array. An author's guard left out of
// that array would silently not run, so the library always puts the definition's extensions in it.
describe("the library owns the extension selection", () => {
	it("runs a guard on every turn, across model changes, template MCP servers, app MCP servers and their failure", async () => {
		const { definition, requests } = guarded();
		const one = fakeModel({ provider: "one", modelId: "m1" });
		one.reply(toolCall("get_time", {}, { id: "c1" }), "first");
		let agent = await startTestAgent(definition, { model: one });
		await agent.send("t", "time?");
		expect(blocked(await resultOf(agent, "t", "c1"))).toBe(true);
		expect(await selection(agent, "t")).toEqual(["assistant", "guard"]);

		// Another model, and the template now has an MCP server (its tools are defined, never connected here).
		const two = fakeModel({ provider: "two", modelId: "m2" });
		two.reply(toolCall("get_time", {}, { id: "c2" }), "second");
		const mcpServers = JSON.stringify([{ slug: "fake", name: "Fake", url: "http://127.0.0.1:9/mcp", tools: [{ name: "echo" }] }]);
		agent = await agent.restart({ model: two, env: { MCP_SERVERS: mcpServers } });
		await agent.send("t", "time now?");
		expect(blocked(await resultOf(agent, "t", "c2"))).toBe(true);
		expect(await selection(agent, "t")).toEqual(["assistant", "guard", "mcp"]);
		const reply = (await agent.settled("t")).messages.at(-1);
		expect(reply?.role === "assistant" && reply.model).toEqual({ provider: "two", id: "m2" });

		// Apps may give threads MCP servers (the platform answers with none here).
		const threadsUrl = await server((res) => res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ data: [] })));
		two.reply(toolCall("get_time", {}, { id: "c3" }), "third");
		agent = await agent.restart({ env: { MCP_SERVERS: mcpServers, MCP_THREADS_URL: `${threadsUrl}/thread-mcp`, MCP_KEY: "k" } });
		await agent.send("t", "and now?");
		expect(blocked(await resultOf(agent, "t", "c3"))).toBe(true);

		// The platform can't be reached for the thread's servers: the fallback selection keeps the guard.
		const failing = await server((res) => res.writeHead(500).end());
		two.reply(toolCall("get_time", {}, { id: "c4" }), "fourth");
		agent = track(await agent.restart({ env: { MCP_THREADS_URL: `${failing}/thread-mcp`, MCP_KEY: "k" } }));
		await agent.send("t", "still?");
		expect(blocked(await resultOf(agent, "t", "c4"))).toBe(true);
		expect(await selection(agent, "t")).toEqual(["assistant", "guard"]);

		// A new thread gets it too.
		two.reply(toolCall("get_time", {}, { id: "c5" }), "fifth");
		await agent.send("u", "time?");
		expect(blocked(await resultOf(agent, "u", "c5"))).toBe(true);
		// The guard saw every model request: two per message (the call, then the answer), five messages.
		expect(requests.length).toBeGreaterThanOrEqual(10);
	});

	it("adds a guard to threads that began before the agent had it", async () => {
		const model = fakeModel();
		model.reply(toolCall("get_time", {}, { id: "before" }), "allowed");
		let agent = await startTestAgent(assistantAgent, { model });
		await agent.send("t", "time?");
		expect((await resultOf(agent, "t", "before")).isError).toBe(false);

		model.reply(toolCall("get_time", {}, { id: "after" }), "blocked");
		agent = track(await agent.restart({ definition: guarded().definition }));
		await agent.send("t", "time again?");
		expect(blocked(await resultOf(agent, "t", "after"))).toBe(true);
	});

	it("applies the guard to a run a crashed process left unfinished, before it resumes", async () => {
		const model = fakeModel();
		const stuck = gate();
		// The first request is in flight when the process dies; the resumed run asks again and calls get_time.
		model.reply(afterGate(stuck, "never"), toolCall("get_time", {}, { id: "resumed" }), "done");
		const agent = await startTestAgent(assistantAgent, { model });
		await agent.send("t", "time?");
		await stuck.reached;
		// No message is sent after the restart: only the selection applied at start can reach the resumed run.
		const restarted = track(await agent.restart({ definition: guarded().definition }));
		expect(blocked(await resultOf(restarted, "t", "resumed"))).toBe(true);
	});
});
