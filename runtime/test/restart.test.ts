import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentDoc, ToolResultEntry } from "@earendil-works/pi-durable";
import { assistantExtension, defineAgent, defineDoc, defineExtension, defineSubagent, defineTool, hook, ToolTask, Type } from "@pdp/runtime";
import { afterGate, expectReplayed, fakeModel, gate, ReplayAssertionError, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const ctx = BACKGROUND_CONTEXT;
const running: TestAgent[] = [];
afterEach(async () => {
	while (running.length > 0) await running.pop()!.close();
});
const track = (agent: TestAgent) => {
	running.push(agent);
	return agent;
};

/** What a replay-safe tool remembers: one entry per call, whatever the number of attempts. */
const Ledger = defineDoc<{ calls: Record<string, string> }>({ kind: "test.ledger", version: 1, scope: "conversation", history: "latest", fork: "initial", initial: () => ({ calls: {} }) });

/** An agent with a replay-safe tool, a tool that isn't, a subagent and a guard; each tool waits at its gate first. */
function agentWith(gates: { remember?: ReturnType<typeof gate>; unsafe?: ReturnType<typeof gate> }) {
	const attempts: string[] = [];
	const remember = defineTool({
		name: "remember",
		description: "Remember a note",
		parameters: Type.Object({ text: Type.String() }),
		replay: "safe",
		execute: async (args, api, context) => {
			attempts.push(api.callId);
			await gates.remember?.wait(context);
			// Idempotent by call: a rerun after a crash finds its own entry.
			await api.commit(async (tx) => {
				const ledger = await tx.doc(Ledger, api.conversationId);
				ledger.calls[api.callId] ??= args.text;
			}, context);
			return { output: [{ type: "text", text: `remembered ${args.text}` }] };
		},
	});
	const unsafe = defineTool({
		name: "send_email",
		description: "Send an email (not safe to repeat)",
		parameters: Type.Object({}),
		execute: async (_args, api, context) => {
			attempts.push(api.callId);
			await gates.unsafe?.wait(context);
			return { output: [{ type: "text", text: "sent" }] };
		},
	});
	const guard = defineExtension({ name: "guard", hooks: [hook(ToolTask, { beforeTool: () => undefined })] });
	const definition = defineAgent({
		documents: [Ledger],
		extensions: ({ instructions }) => [
			assistantExtension(instructions),
			defineExtension({ name: "notes", tools: [remember, unsafe] }),
			defineSubagent({ name: "delegate", description: "Give a self-contained task to a subagent" }),
			guard,
		],
	});
	return { definition, attempts };
}

describe("the test kit's restart helper", () => {
	it("restarts in the middle of a turn: the request is made again, the input isn't", async () => {
		const model = fakeModel();
		const inFlight = gate();
		model.reply(afterGate(inFlight, "lost with the process"), "answer after the restart");
		const first = await startTestAgent(agentWith({}).definition, { model });
		await first.send("t", "hello", { requestId: "m1" });
		await inFlight.reached;
		const second = track(await first.restart());
		// The platform retries the message after the restart: the request ID finds the first submission.
		await second.send("t", "hello", { requestId: "m1" });
		const view = await second.settled("t");
		expect(view.messages.map((m) => m.role)).toEqual(["user", "assistant"]);
		expect(view.messages[1]).toMatchObject({ text: "answer after the restart" });
		expect(await expectReplayed(second, "t")).toEqual({ toolCalls: 0, subagents: 0 });
	});

	it("restarts in the middle of a replay-safe tool call: it reruns once and its effect is kept once", async () => {
		const model = fakeModel();
		const inTool = gate();
		const { definition, attempts } = agentWith({ remember: inTool });
		model.reply(toolCall("remember", { text: "milk" }, { id: "r1" }), "Noted.");
		const first = await startTestAgent(definition, { model });
		await first.send("t", "remember milk");
		await inTool.reached;
		const second = track(await first.restart());
		inTool.open();
		const view = await second.settled("t");
		expect(view.messages.find((m) => m.role === "tool")).toMatchObject({ toolCallId: "r1", text: "remembered milk", isError: false });
		expect(attempts).toEqual(["r1", "r1"]);
		const conversation = (await second.agent.threads.get("t"))!;
		expect(await second.harness.snapshot(Ledger, conversation.id, ctx)).toEqual({ calls: { r1: "milk" } });
		expect(await expectReplayed(second, "t")).toEqual({ toolCalls: 1, subagents: 0 });
	});

	it("reports a tool that isn't replay-safe and was interrupted as lost, unless that is expected", async () => {
		const model = fakeModel();
		const inTool = gate();
		const { definition, attempts } = agentWith({ unsafe: inTool });
		model.reply(toolCall("send_email", {}, { id: "e1" }), "It may not have been sent.");
		const first = await startTestAgent(definition, { model });
		await first.send("t", "send it");
		await inTool.reached;
		const second = track(await first.restart());
		const view = await second.settled("t");
		// Never run twice: the model is told the call was interrupted.
		expect(attempts).toEqual(["e1"]);
		expect(view.messages.find((m) => m.role === "tool")).toMatchObject({ toolCallId: "e1", isError: true });
		await expect(expectReplayed(second, "t")).rejects.toThrow(/e1 \(send_email\) ended interrupted/);
		expect(await expectReplayed(second, "t", { allowInterrupted: true })).toEqual({ toolCalls: 1, subagents: 0 });
	});

	it("restarts in the middle of a subagent: the child is given its task once and its answer reaches the parent", async () => {
		const model = fakeModel();
		const inChild = gate();
		model.reply(
			toolCall("delegate", { task: "Name three primes." }, { id: "s1" }),
			// The child's first request is in flight when the process dies; the resumed child asks again.
			afterGate(inChild, "lost with the process"),
			"2, 3 and 5.",
			"The subagent says: 2, 3 and 5.",
		);
		const first = await startTestAgent(agentWith({}).definition, { model });
		await first.send("t", "find primes with a subagent");
		await inChild.reached;
		const second = track(await first.restart());
		const view = await second.settled("t");
		expect(view.messages.find((m) => m.role === "tool")).toMatchObject({ toolCallId: "s1", text: "2, 3 and 5.", isError: false });
		expect(view.messages.at(-1)).toMatchObject({ text: "The subagent says: 2, 3 and 5." });
		expect(await expectReplayed(second, "t")).toEqual({ toolCalls: 1, subagents: 1 });

		// The child selects the caller's extensions, guard included, but not the subagent tool itself.
		const parent = (await second.agent.threads.get("t"))!;
		const childId = await childOf(second, parent.id);
		expect((await second.harness.snapshot(AgentDoc, childId as never, ctx))?.extensions).toEqual(["assistant", "notes", "guard"]);
		// Declared documents exist in a subagent's conversation too.
		expect(await second.harness.snapshot(Ledger, childId as never, ctx)).toEqual({ calls: {} });
	});

	it("finds a repeated tool result", async () => {
		const model = fakeModel();
		model.reply(toolCall("remember", { text: "x" }, { id: "d1" }), "done");
		const agent = track(await startTestAgent(agentWith({}).definition, { model }));
		await agent.send("t", "remember x");
		await agent.settled("t");
		const conversation = (await agent.agent.threads.get("t"))!;
		await agent.harness.commit(async (tx) => {
			const message = { role: "toolResult" as const, toolCallId: "d1", toolName: "remember", content: [{ type: "text" as const, text: "again" }], isError: false, timestamp: Date.now() };
			await tx.appendEntry(ToolResultEntry, conversation.id, { model: [message], data: { diagnostics: [] } } as never);
		}, ctx);
		const failure = await expectReplayed(agent, "t").catch((error: unknown) => error);
		expect(failure).toBeInstanceOf(ReplayAssertionError);
		expect((failure as ReplayAssertionError).problems.join("\n")).toMatch(/d1 \(remember\) has 2 results \(repeated\)/);
	});
});

/** The conversation the subagent tool created in a thread: named in its result's details. */
async function childOf(agent: TestAgent, parent: number): Promise<number> {
	const conversation = await agent.harness.conversation(parent as never, ctx);
	const watch = await conversation!.watch(ctx);
	const entries = watch.value.entries;
	await watch.stop();
	for (const entry of entries) {
		for (const message of entry.model ?? []) {
			const id = message.role === "toolResult" ? (message.details as { conversationId?: number } | undefined)?.conversationId : undefined;
			if (typeof id === "number") return id;
		}
	}
	throw new Error("no subagent");
}
