// The deep researcher (docs/first-party-agents.md): writes a plan, runs a foreground and a background subagent at once,
// records sources, answers with a report once the background answer is back, and none of it is repeated or lost
// across a crash while both subagents work. A run a trigger started keeps its subagents in the foreground.
import type { AssistantMessage, Message } from "@earendil-works/pi-ai";
import { type FauxResponseFactory, fauxAssistantMessage, fauxToolCall } from "@earendil-works/pi-ai/providers/faux";
import { SUBAGENT_REPORT } from "@pdp/runtime";
import { agentForKind, type ResearchPlan, type ResearchSources } from "@pdp/runtime/kinds";
import { answer, expectReplayed, fakeModel, type Gate, gate, startTestAgent, type TestAgent } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const running: TestAgent[] = [];
afterEach(async () => {
	while (running.length > 0) await running.pop()!.close().catch(() => {});
});
const track = (agent: TestAgent) => {
	running.push(agent);
	return agent;
};

const QUESTION = "Research: how do durable agents survive restarts?";
const QUICK_ANSWER = "Commits are atomic.\nSources:\nhttps://a.example/commits | Commits | commits are atomic";
const DEEP_ANSWER = "Tasks resume from checkpoints.\nSources:\nhttps://b.example/tasks | Tasks | tasks resume from checkpoints";
const REPORT = "Report: durable agents survive restarts because commits are atomic [1] and tasks resume from checkpoints [2].";

const textOf = (m: Message | undefined) =>
	m === undefined ? "" : typeof m.content === "string" ? m.content : m.content.flatMap((c) => (c.type === "text" ? [c.text] : [])).join("");
const calls = (...list: [string, Record<string, unknown>, string][]): AssistantMessage =>
	fauxAssistantMessage(list.map(([name, args, id]) => fauxToolCall(name, args as Parameters<typeof fauxToolCall>[1], { id })), { stopReason: "toolUse" });

/**
 * The model, for the researcher and its subagents at once (their requests run in parallel, so it answers by what each
 * request is about, not in queue order). Subagents wait for their gate.
 */
function researchModel(gates: { quick: Gate; deep: Gate }) {
	const route: FauxResponseFactory = async (context, options) => {
		const messages = context.messages as Message[];
		const first = textOf(messages.find((m) => m.role === "user"));
		if (first.startsWith("QUICK:")) {
			await gates.quick.wait(options?.signal);
			return answer(QUICK_ANSWER);
		}
		if (first.startsWith("DEEP:")) {
			await gates.deep.wait(options?.signal);
			return answer(DEEP_ANSWER);
		}
		const last = messages.at(-1);
		if (last?.role === "user") {
			if (textOf(last).startsWith("[Background subagent research_deep")) {
				return calls(
					["research_source", { url: "https://b.example/tasks", title: "Tasks", supports: "tasks resume from checkpoints", step: "s2" }, "src2"],
					["research_step", { id: "s2", status: "done" }, "st2"],
				);
			}
			return calls(["research_plan", { question: "How do durable agents survive restarts?", steps: ["Commits", "Tasks"] }, "p1"]);
		}
		// The ids of the round's results (the trailing tool results).
		const ids = new Set<string>();
		for (let i = messages.length - 1; i >= 0 && messages[i]!.role === "toolResult"; i--) ids.add((messages[i] as { toolCallId: string }).toolCallId);
		if (ids.has("p1")) {
			return calls(
				["research_quick", { task: "QUICK: are commits atomic?" }, "q1"],
				["research_deep", { task: "DEEP: how do tasks resume?" }, "d1"],
				["research_step", { id: "s1", status: "running", subagent: "quick" }, "st0"],
			);
		}
		if (ids.has("q1")) {
			return calls(
				["research_source", { url: "https://a.example/commits", title: "Commits", supports: "commits are atomic", step: "s1" }, "src1"],
				["research_step", { id: "s1", status: "done" }, "st1"],
			);
		}
		if (ids.has("src1")) return answer("Step s1 is done; the answer for s2 is on its way.");
		if (ids.has("src2")) return answer(REPORT);
		return answer("ok");
	};
	const model = fakeModel();
	for (let i = 0; i < 40; i++) model.reply(route);
	return model;
}

async function until<T>(what: string, check: () => Promise<T | undefined>, timeoutMs = 20_000): Promise<T> {
	const deadline = Date.now() + timeoutMs;
	for (;;) {
		const value = await check();
		if (value !== undefined) return value;
		if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
		await new Promise((r) => setTimeout(r, 25));
	}
}

const reportDone = (agent: TestAgent) =>
	until("the report", async () => {
		const view = await agent.view("t");
		const last = view?.messages.at(-1);
		return view && !view.busy && last?.role === "assistant" && last.text === REPORT ? view : undefined;
	});

describe("the deep researcher", () => {
	it("plans, runs a foreground and a background subagent at once, records sources and reports; restart-safe", async () => {
		const gates = { quick: gate(), deep: gate() };
		const model = researchModel(gates);
		let agent = track(await startTestAgent(agentForKind("researcher"), { model }));
		await agent.send("t", QUESTION, { author: { kind: "user", id: "u1", name: "Ann" } });

		// Both subagents are working at the same time, and the plan is exposed.
		await Promise.all([gates.quick.reached, gates.deep.reached]);
		const plan = (await agent.document("t", "research.plan"))?.value as ResearchPlan;
		expect(plan.question).toBe("How do durable agents survive restarts?");
		expect(plan.steps.map((s) => [s.id, s.text])).toEqual([
			["s1", "Commits"],
			["s2", "Tasks"],
		]);
		expect((await agent.status()).backgroundWork).toMatchObject({ tasks: 1 });

		// The process dies while both subagents wait on the model; the next one resumes both.
		agent = track(await agent.restart());
		gates.quick.open();
		const first = await agent.settled("t");
		expect(first.messages.at(-1)).toMatchObject({ role: "assistant", text: "Step s1 is done; the answer for s2 is on its way." });
		// The background subagent is still at work, and keeps the agent awake.
		expect((await agent.status()).backgroundWork).toMatchObject({ tasks: 1 });
		const deep = first.messages.find((m) => m.role === "tool" && m.toolCallId === "d1");
		expect(deep).toMatchObject({ isError: false, details: { background: true } });

		gates.deep.open();
		const view = await reportDone(agent);
		// The background answer came back once, as a message, and started the run that wrote the report.
		const reports = view.messages.filter((m) => m.role === "user" && m.text.startsWith("[Background subagent research_deep"));
		expect(reports).toHaveLength(1);
		expect(reports[0]!.role === "user" && reports[0]!.text).toContain("Tasks resume from checkpoints.");
		const sources = (await agent.document("t", "research.sources"))?.value as ResearchSources;
		expect(sources.sources).toEqual([
			{ url: "https://a.example/commits", title: "Commits", supports: ["commits are atomic"], step: "s1" },
			{ url: "https://b.example/tasks", title: "Tasks", supports: ["tasks resume from checkpoints"], step: "s2" },
		]);
		expect(((await agent.document("t", "research.plan"))?.value as ResearchPlan).steps.map((s) => s.status)).toEqual(["done", "done"]);
		expect(view.documents?.["research.sources"]?.revision).toBe((await agent.document("t", "research.sources"))?.revision);
		const summary = await expectReplayed(agent, "t");
		expect(summary.subagents).toBe(2);
		await until("no background work", async () => (((await agent.status()).backgroundWork as { tasks: number }).tasks === 0 ? true : undefined));

		// The documents survive a restart.
		agent = track(await agent.restart({ graceful: true }));
		expect(((await agent.document("t", "research.sources"))?.value as ResearchSources).sources).toHaveLength(2);
		expect(SUBAGENT_REPORT).toBe("subagent.report");
	}, 60_000);

	it("keeps a triggered run's subagents in the foreground, so its run time and budget cover them", async () => {
		const gates = { quick: gate(), deep: gate() };
		gates.quick.open();
		gates.deep.open();
		const model = researchModel(gates);
		const agent = track(await startTestAgent(agentForKind("researcher"), { model }));
		await agent.send("t", QUESTION, { author: { kind: "trigger", id: "tr_1", name: "Weekly research" }, announceAuthor: true });
		const view = await agent.settled("t");
		const deep = view.messages.find((m) => m.role === "tool" && m.toolCallId === "d1");
		// It waited for the answer: the result carries it, and nothing comes back later.
		expect(deep).toMatchObject({ isError: false });
		expect(deep?.role === "tool" && deep.text).toContain("Tasks resume from checkpoints.");
		expect(deep?.role === "tool" && (deep.details as { background?: boolean }).background).toBeUndefined();
		expect(view.messages.some((m) => m.role === "user" && m.text.startsWith("[Background subagent"))).toBe(false);
		await expectReplayed(agent, "t");
	}, 30_000);

	it("tells the thread when the platform aborts a background subagent at the ceiling", async () => {
		const gates = { quick: gate(), deep: gate() };
		gates.quick.open();
		const agent = track(await startTestAgent(agentForKind("researcher"), { model: researchModel(gates) }));
		await agent.send("t", QUESTION);
		await agent.settled("t");
		await gates.deep.reached;
		const res = await fetch(`${agent.baseUrl}/background/abort`, {
			method: "POST",
			headers: { authorization: `Bearer ${agent.token}`, "content-type": "application/json" },
			body: JSON.stringify({ olderThanMs: 0 }),
		});
		expect(((await res.json()) as { data: { aborted: number } }).data.aborted).toBe(1);
		const view = await reportDone(agent);
		const ended = view.messages.filter((m) => m.role === "user" && m.text.startsWith("[Background subagent research_deep (call d1) ended]"));
		expect(ended).toHaveLength(1);
		expect(ended[0]!.role === "user" && ended[0]!.text).toContain("stopped before it answered");
	}, 30_000);

	it("waits for research_deep when background subagents are off", async () => {
		const gates = { quick: gate(), deep: gate() };
		gates.quick.open();
		gates.deep.open();
		const agent = track(await startTestAgent(agentForKind("researcher", '{"backgroundSubagents":false}'), { model: researchModel(gates) }));
		await agent.send("t", QUESTION);
		const view = await agent.settled("t");
		expect(view.messages.find((m) => m.role === "tool" && m.toolCallId === "d1")).toMatchObject({ isError: false });
		expect(view.messages.some((m) => m.role === "user" && m.text.startsWith("[Background subagent"))).toBe(false);
	}, 30_000);
});
