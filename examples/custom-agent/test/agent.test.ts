// The agent's own tests, with the library's test kit (@pdp/runtime/testing): a fake model, in-memory storage and the
// agent's HTTP API on a loopback port, as the platform calls it. No database or model key needed.
import { assistantExtension, defineAgent, defineExtension, wrapTool } from "@pdp/runtime";
import { afterGate, expectReplayed, fakeModel, gate, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";
import { type Board, type BoardDetails, boardExtension, boardTool, taskBoardAgent } from "../src/agent.ts";

const running: TestAgent[] = [];
afterEach(async () => {
	while (running.length > 0) await running.pop()!.close();
});
const track = (agent: TestAgent) => {
	running.push(agent);
	return agent;
};

describe("the task board", () => {
	it("adds a task with its tool, returns details for apps, and exposes the board", async () => {
		const model = fakeModel();
		model.reply(toolCall("board", { action: "add", text: "Buy milk" }, { id: "c1" }), "Added it.");
		const agent = track(await startTestAgent(taskBoardAgent, { model }));
		await agent.send("t1", "remind me to buy milk");
		const view = await agent.settled("t1");

		const result = view.messages.find((m) => m.role === "tool");
		expect(result).toMatchObject({ name: "board", isError: false, details: { action: "add", open: 1, done: 0 } satisfies BoardDetails });
		// What an app reads through the platform, and the revision the thread's view and events carry.
		const board = await agent.document("t1", "example.board");
		expect(board?.value).toEqual({ tasks: [{ id: "c1", text: "Buy milk", done: false }] } satisfies Board);
		expect(view.documents?.["example.board"]?.revision).toBe(board?.revision);
		expect(view.messages.at(-1)).toMatchObject({ role: "assistant", text: "Added it." });
	});

	it("keeps each conversation's board apart", async () => {
		const model = fakeModel();
		model.reply(toolCall("board", { action: "add", text: "Ship it" }, { id: "c1" }), "Added.");
		const agent = track(await startTestAgent(taskBoardAgent, { model }));
		await agent.send("a", "add ship it");
		await agent.settled("a");
		await agent.send("b", "hello");
		await agent.settled("b");
		expect((await agent.document("b", "example.board"))?.value).toEqual({ tasks: [] });
	});

	it("blocks clearing the board: the guard runs before the tool", async () => {
		const model = fakeModel();
		model.reply(
			toolCall("board", { action: "add", text: "Buy milk" }, { id: "c1" }),
			"Added.",
			toolCall("board", { action: "clear" }, { id: "c2" }),
			"I can't clear it myself.",
		);
		const agent = track(await startTestAgent(taskBoardAgent, { model }));
		await agent.send("t1", "add milk");
		await agent.settled("t1");
		await agent.send("t1", "clear the board");
		const view = await agent.settled("t1", { messages: 8 });
		const blocked = view.messages.find((m) => m.role === "tool" && m.toolCallId === "c2");
		expect(blocked).toMatchObject({ isError: true });
		expect(blocked?.role === "tool" && blocked.text).toContain("needs a person");
		expect((await agent.document("t1", "example.board"))?.value).toMatchObject({ tasks: [{ text: "Buy milk" }] });
	});
});

describe("restarts", () => {
	it("neither repeats nor loses the tool's write when the process dies in the middle of the call", async () => {
		// A test-only extension pauses the board tool until the test opens the gate.
		const inTool = gate();
		const pause = defineExtension({
			name: "test-pause",
			wraps: [wrapTool(boardTool, (tool) => ({ ...tool, execute: async (args, api, context) => (await inTool.wait(context), tool.execute(args, api, context)) }))],
		});
		const definition = defineAgent({ ...taskBoardAgent, extensions: ({ instructions }) => [assistantExtension(instructions), boardExtension, pause] });
		const model = fakeModel();
		model.reply(toolCall("board", { action: "add", text: "Buy milk" }, { id: "c1" }), "Added.");
		const first = await startTestAgent(definition, { model });
		await first.send("t1", "add milk");
		await inTool.reached;
		const second = track(await first.restart()); // the process dies in the tool, and starts again on the same storage
		inTool.open();
		await second.settled("t1");
		await expectReplayed(second, "t1"); // one result for c1, nothing lost or repeated
		expect((await second.document("t1", "example.board"))?.value).toEqual({ tasks: [{ id: "c1", text: "Buy milk", done: false }] });
	});

	it("keeps the board when the process dies while the model answers", async () => {
		const answering = gate();
		const model = fakeModel();
		model.reply(toolCall("board", { action: "add", text: "Buy milk" }, { id: "c1" }), afterGate(answering, "lost with the process"), "Added after the restart.");
		const first = await startTestAgent(taskBoardAgent, { model });
		await first.send("t1", "add milk");
		await answering.reached;
		const second = track(await first.restart());
		const view = await second.settled("t1");
		await expectReplayed(second, "t1");
		expect(view.messages.at(-1)).toMatchObject({ text: "Added after the restart." });
		expect((await second.document("t1", "example.board"))?.value).toMatchObject({ tasks: [{ id: "c1" }] });
	});
});
