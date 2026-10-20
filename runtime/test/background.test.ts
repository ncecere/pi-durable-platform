/*
 * Background-aware hibernation (docs/hibernation.md): /status reports live background work from pi-durable's task
 * graph, POST /background/abort aborts what is older than the platform's ceiling (never a thread's reply), and work
 * waiting for a person keeps nothing awake.
 */
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { defineAgent, defineExtension, defineTask, defineTool, Type } from "@pdp/runtime";
import { afterGate, fakeModel, gate, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const ctx = BACKGROUND_CONTEXT;
const running: TestAgent[] = [];
afterEach(async () => {
	while (running.length > 0) await running.pop()!.close();
});

/** Runs until aborted: a background job that never ends by itself. */
const Long = defineTask<null, { phase: "run" }, null>({
	name: "acme.long",
	version: 1,
	initial: () => ({ phase: "run" }),
	phases: {
		run: async (_task, runtime) => {
			await new Promise<void>((resolve) => runtime.signal.addEventListener("abort", () => resolve(), { once: true }));
		},
	},
	abort: (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

/** Ends when the test says so. */
const finish = gate();
const Short = defineTask<null, { phase: "run" }, null>({
	name: "acme.short",
	version: 1,
	initial: () => ({ phase: "run" }),
	phases: {
		run: async (_task, runtime, context) => {
			await finish.wait(runtime.signal);
			await runtime.commit(() => ({ status: "terminal", outcome: { status: "completed", result: null } }), context);
		},
	},
	abort: (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

/** Stands for a question to a person: live until answered (here: never), and declared in `waitsForPerson`. */
const Ask = defineTask<null, { phase: "wait" }, null>({
	name: "acme.ask",
	version: 1,
	initial: () => ({ phase: "wait" }),
	phases: {
		wait: async (_task, runtime) => {
			await new Promise<void>((resolve) => runtime.signal.addEventListener("abort", () => resolve(), { once: true }));
		},
	},
	abort: (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

/** A background job that asks a person and waits durably for the answer (a `waiting` state on the question). */
const Asker = defineTask<null, { phase: "ask" } | { phase: "answered" }, null>({
	name: "acme.asker",
	version: 1,
	initial: () => ({ phase: "ask" }),
	phases: {
		ask: (task, runtime, context) =>
			runtime.commit(async (tx) => {
				const question = await tx.createTask(Ask, null, { ownership: { kind: "task", taskId: task.id } });
				return { status: "waiting", checkpoint: { phase: "answered" }, on: [question], policy: "allSettled" };
			}, context),
		answered: (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "completed", result: null } }), context),
	},
	abort: (_task, runtime, context) => runtime.commit(() => ({ status: "terminal", outcome: { status: "aborted" } }), context),
});

/** A tool that asks a person and awaits the answer in the reply (as an approval would). */
const askTool = defineTool({
	name: "ask_person",
	description: "Ask a person",
	parameters: Type.Object({}),
	replay: "safe",
	execute: async (_args, api, context) => {
		const question = await api.createTask(Ask, null, { ownership: { kind: "task", taskId: api.taskId } }, context);
		await api.waitForTask(question, context);
		return { output: [{ type: "text", text: "answered" }] };
	},
});

const definition = defineAgent({
	extensions: [defineExtension({ name: "acme", tasks: [Long, Short, Ask, Asker], tools: [askTool] })],
	waitsForPerson: ["acme.ask"],
});

type Background = { tasks: number; oldestStartedAt: number | null; waitingForPerson: number };
type Status = { busyThreads: number; lastActivityAt: number; backgroundWork: Background };

async function start(model = fakeModel()) {
	const agent = await startTestAgent(definition, { model });
	running.push(agent);
	const status = async () => (await agent.status()) as unknown as Status;
	const abort = async (olderThanMs: number) => {
		const res = await fetch(`${agent.baseUrl}/background/abort`, {
			method: "POST",
			headers: { authorization: `Bearer ${agent.token}`, "content-type": "application/json" },
			body: JSON.stringify({ olderThanMs }),
		});
		expect(res.status).toBe(200);
		return ((await res.json()) as { data: { aborted: number; settled: boolean } }).data;
	};
	/** A thread's conversation (the thread answered first). */
	const thread = async (id: string) => {
		await agent.send(id, "hello");
		await agent.settled(id);
		return (await agent.agent.threads.get(id))!;
	};
	return { agent, status, abort, thread };
}

async function until<T>(read: () => Promise<T>, ok: (v: T) => boolean, what: string): Promise<T> {
	let last: T | undefined;
	for (let i = 0; i < 200; i++) {
		last = await read();
		if (ok(last)) return last;
		await new Promise((r) => setTimeout(r, 25));
	}
	throw new Error(`${what}: ${JSON.stringify(last)}`);
}

describe("/status reports background work", () => {
	it("counts a background task while it runs, with its start time, and none once it ends", async () => {
		const { status, thread } = await start();
		const conversation = await thread("t1");
		expect((await status()).backgroundWork).toEqual({ tasks: 0, oldestStartedAt: null, waitingForPerson: 0 });

		const before = Date.now();
		await conversation.commit((tx) => tx.createTask(Short, null, { ownership: { kind: "conversation" }, background: true }), ctx);
		const busy = await until(status, (s) => s.backgroundWork.tasks === 1, "the background task is reported");
		expect(busy.busyThreads).toBe(0); // not a thread's reply
		expect(busy.backgroundWork.oldestStartedAt).toBeGreaterThanOrEqual(before - 5);
		expect(busy.backgroundWork.oldestStartedAt).toBeLessThanOrEqual(Date.now());

		// Its end counts as activity: the idle clock starts there.
		const ended = Date.now();
		finish.open();
		const done = await until(status, (s) => s.backgroundWork.tasks === 0, "the task ended");
		expect(done.backgroundWork.oldestStartedAt).toBeNull();
		expect(done.lastActivityAt).toBeGreaterThanOrEqual(ended - 5);
	});

	it("counts work at the top of a conversation that is not a thread (a background subagent's run)", async () => {
		const { agent, status } = await start();
		const other = await agent.harness.createConversation({ ownership: { kind: "ownerless" } }, ctx);
		await other.commit((tx) => tx.createTask(Long, null, { ownership: { kind: "conversation" } }), ctx);
		const s = await until(status, (v) => v.backgroundWork.tasks === 1, "the task is reported");
		expect(s.busyThreads).toBe(0);
	});

	it("does not count a thread's reply as background work", async () => {
		const model = fakeModel();
		const hold = gate();
		model.reply(afterGate(hold, "done"));
		const { agent, status, abort } = await start(model);
		await agent.send("t1", "hello");
		await hold.reached;
		const s = await until(status, (v) => v.busyThreads === 1, "the reply is busy");
		expect(s.backgroundWork.tasks).toBe(0);
		// Nor does the ceiling's abort touch it.
		expect((await abort(0)).aborted).toBe(0);
		hold.open();
		expect((await agent.settled("t1")).messages.at(-1)?.text).toBe("done");
	});
});

describe("POST /background/abort", () => {
	it("aborts background work older than the age, not younger work, without counting as activity", async () => {
		const { status, abort, thread } = await start();
		const conversation = await thread("t1");
		await conversation.commit((tx) => tx.createTask(Long, null, { ownership: { kind: "conversation" }, background: true }), ctx);
		const before = await until(status, (s) => s.backgroundWork.tasks === 1, "the task is reported");

		expect(await abort(60 * 60 * 1000)).toEqual({ aborted: 0, settled: true }); // younger than an hour
		expect((await status()).backgroundWork.tasks).toBe(1);

		await new Promise((r) => setTimeout(r, 20));
		expect(await abort(10)).toEqual({ aborted: 1, settled: true });
		const after = await status();
		expect(after.backgroundWork.tasks).toBe(0);
		// The platform hibernates the agent right after: the abort is not activity.
		expect(after.lastActivityAt).toBe(before.lastActivityAt);
	});
});

describe("work waiting for a person", () => {
	it("keeps nothing awake: a question task, and a background job waiting on one", async () => {
		const { status, thread } = await start();
		const conversation = await thread("t1");
		await conversation.commit((tx) => tx.createTask(Ask, null, { ownership: { kind: "conversation" }, background: true }), ctx);
		await conversation.commit((tx) => tx.createTask(Asker, null, { ownership: { kind: "conversation" }, background: true }), ctx);
		const s = await until(status, (v) => v.backgroundWork.waitingForPerson === 2, "both questions are seen");
		expect(s.backgroundWork.tasks).toBe(0);
		expect(s.busyThreads).toBe(0);
	});

	it("does not count a reply that only waits for a person as a busy thread", async () => {
		const model = fakeModel();
		model.reply(toolCall("ask_person"));
		const { agent, status } = await start(model);
		await agent.send("t1", "please ask");
		const s = await until(status, (v) => v.backgroundWork.waitingForPerson === 1, "the question is seen");
		expect((await agent.view("t1"))?.busy).toBe(true); // the reply is in progress...
		expect(s.busyThreads).toBe(0); // ...but waits only for the person
		expect(s.backgroundWork.tasks).toBe(0);
	});
});
