// The triage kind on a fake MCP tracker (docs/first-party-agents.md): it reads new items through the tools its settings
// name, records them in its exposed queue, drafts a comment, waits for a person's approval before writing, writes only
// when approved, refuses tools its settings don't name, and its queue and its wait survive restarts.
import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { agentForKind, type TriageQueue } from "@pdp/runtime/kinds";
import { expectReplayed, fakeModel, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";

const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});
const track = (agent: TestAgent) => {
	cleanup.push(() => agent.close());
	return agent;
};

type Issue = { id: number; title: string; body: string; url: string };

/** A tracker as a Streamable HTTP MCP server (JSON answers): list, read, comment, and a delete nobody named. */
async function fakeTracker(issues: Issue[]) {
	const calls: { tool: string; args: Record<string, unknown> }[] = [];
	const comments: { id: number; body: string }[] = [];
	const read = (req: IncomingMessage) =>
		new Promise<string>((resolve) => {
			let body = "";
			req.on("data", (c: Buffer) => (body += c.toString()));
			req.on("end", () => resolve(body));
		});
	const server: Server = createServer(async (req, res) => {
		const msg = JSON.parse(await read(req)) as { id?: number; method: string; params?: { name?: string; arguments?: Record<string, unknown> } };
		if (msg.id === undefined) {
			res.writeHead(202).end();
			return;
		}
		let result: unknown;
		if (msg.method === "initialize") {
			result = { protocolVersion: "2025-06-18", capabilities: { tools: {} }, serverInfo: { name: "tracker", version: "1" } };
		} else if (msg.method === "tools/call") {
			const tool = msg.params?.name ?? "";
			const args = msg.params?.arguments ?? {};
			calls.push({ tool, args });
			const text = (t: string) => ({ content: [{ type: "text", text: t }] });
			if (tool === "list_issues") result = text(JSON.stringify(issues.map(({ id, title, url }) => ({ id, title, url }))));
			else if (tool === "get_issue") result = text(JSON.stringify(issues.find((i) => i.id === Number(args.id)) ?? null));
			else if (tool === "add_comment") {
				comments.push({ id: Number(args.id), body: String(args.body) });
				result = text("Commented.");
			} else result = text("done");
		} else {
			result = {};
		}
		res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ jsonrpc: "2.0", id: msg.id, result }));
	});
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => server.close(resolve)));
	const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}/mcp`;
	const obj = { type: "object", properties: { id: { type: "number" }, body: { type: "string" } } };
	const env = {
		MCP_SERVERS: JSON.stringify([
			{
				slug: "tracker",
				name: "Tracker",
				url,
				tools: [
					{ name: "list_issues", description: "List new issues", inputSchema: { type: "object", properties: {} } },
					{ name: "get_issue", description: "Read an issue", inputSchema: obj },
					{ name: "add_comment", description: "Comment on an issue", inputSchema: obj },
					{ name: "delete_issue", description: "Delete an issue", inputSchema: obj },
				],
			},
		]),
	};
	return { env, calls, comments };
}

const SETTINGS = JSON.stringify({
	listTools: ["mcp__tracker__list_issues"],
	readTools: ["mcp__tracker__get_issue"],
	writeTools: ["mcp__tracker__add_comment"],
});
const triage = agentForKind("triage", SETTINGS);
const ISSUES: Issue[] = [{ id: 7, title: "Crash on start", body: "It crashes when I open it.", url: "https://tracker.example/7" }];
const REPLY = "Thanks for the report: could you share the version and the log?";

/** The model's side of one triage of issue 7, up to the comment (which waits for approval), then `after`. */
function script(model: ReturnType<typeof fakeModel>, after: ReturnType<typeof toolCall>[], answer: string) {
	model.reply(
		toolCall("mcp__tracker__list_issues", {}, { id: "l1" }),
		toolCall("triage_record", { id: "7", title: "Crash on start", url: "https://tracker.example/7", status: "new" }, { id: "r1" }),
		toolCall("mcp__tracker__get_issue", { id: 7 }, { id: "g1" }),
		toolCall("triage_record", { id: "7", status: "drafted", decision: "bug, needs info", labels: ["bug"], reply: REPLY }, { id: "r2" }),
		toolCall("mcp__tracker__add_comment", { id: 7, body: REPLY }, { id: "c1" }),
		...after,
		answer,
	);
}

const queueOf = async (agent: TestAgent, thread: string) => (await agent.document(thread, "triage.queue"))?.value as TriageQueue | undefined;

describe("the triage kind on a fake MCP tracker", () => {
	it("reads new items, queues them, drafts a comment, asks first and comments once approved; the queue survives a restart", async () => {
		const tracker = await fakeTracker(ISSUES);
		const model = fakeModel();
		script(model, [toolCall("triage_record", { id: "7", status: "done" }, { id: "r3" })], "Triaged #7: a bug; asked for the version and log.");
		let agent = track(await startTestAgent(triage, { model, env: tracker.env }));
		await agent.send("hook", "[Message from trigger New issue] Issue #7 was opened.", { author: { kind: "trigger", id: "tr_1", name: "New issue" } });

		// The comment waits for a person; nothing was written yet, and the queue already has the item and its draft.
		const pending = await agent.pendingApproval("hook");
		expect(pending).toMatchObject({ tool: "mcp__tracker__add_comment", summary: "Triage wants to write with mcp__tracker__add_comment" });
		expect(tracker.calls.map((c) => c.tool)).toEqual(["list_issues", "get_issue"]);
		expect(tracker.comments).toEqual([]);
		expect((await queueOf(agent, "hook"))?.items).toEqual([
			expect.objectContaining({ id: "7", title: "Crash on start", status: "drafted", labels: ["bug"], reply: REPLY, url: "https://tracker.example/7" }),
		]);
		expect((await agent.status()).waitingThreads).toBe(1);

		// The process dies while it waits; the wait is in storage, and the decision lands after the restart.
		agent = track(await agent.restart());
		expect(await agent.decide(pending.id, "approve", { decidedBy: { kind: "user", id: "u1", name: "Ann" } })).toBe(200);
		const view = await agent.settled("hook");
		expect(view.messages.at(-1)).toMatchObject({ role: "assistant", text: "Triaged #7: a bug; asked for the version and log." });
		expect(tracker.comments).toEqual([{ id: 7, body: REPLY }]);
		await expectReplayed(agent, "hook");
		// The view says the queue changed (its revision), and apps read it.
		const queue = await agent.document("hook", "triage.queue");
		expect(view.documents?.["triage.queue"]?.revision).toBe(queue?.revision);
		expect((queue?.value as TriageQueue).items[0]).toMatchObject({ id: "7", status: "done" });

		// The queue survives a crash and a graceful stop.
		agent = track(await agent.restart());
		expect((await queueOf(agent, "hook"))?.items[0]).toMatchObject({ id: "7", status: "done", reply: REPLY });
		agent = track(await agent.restart({ graceful: true }));
		expect((await queueOf(agent, "hook"))?.items).toHaveLength(1);
		expect(tracker.comments).toHaveLength(1);
	}, 30_000);

	it("doesn't comment after a denial, and the model is told why", async () => {
		const tracker = await fakeTracker(ISSUES);
		const model = fakeModel();
		script(model, [toolCall("triage_record", { id: "7", status: "triaged" }, { id: "r3" })], "Drafted a reply for #7; the comment was denied.");
		const agent = track(await startTestAgent(triage, { model, env: tracker.env }));
		await agent.send("sweep", "Sweep the tracker.");
		const pending = await agent.pendingApproval("sweep");
		expect(await agent.decide(pending.id, "deny", { reason: "not yet", decidedBy: { kind: "user", id: "u1", name: "Ann" } })).toBe(200);
		const view = await agent.settled("sweep");
		const denied = view.messages.find((m) => m.role === "tool" && m.toolCallId === "c1");
		expect(denied).toMatchObject({ isError: true });
		expect(denied?.role === "tool" && denied.text).toContain("not yet");
		expect(tracker.comments).toEqual([]);
		expect(tracker.calls.map((c) => c.tool)).not.toContain("add_comment");
		expect((await queueOf(agent, "sweep"))?.items[0]).toMatchObject({ id: "7", status: "triaged", reply: REPLY });
	}, 30_000);

	it("refuses MCP tools its settings don't name", async () => {
		const tracker = await fakeTracker(ISSUES);
		const model = fakeModel();
		model.reply(toolCall("mcp__tracker__delete_issue", { id: 7 }, { id: "d1" }), "I can't delete it.");
		const agent = track(await startTestAgent(triage, { model, env: tracker.env }));
		await agent.send("t", "Delete issue 7.");
		const view = await agent.settled("t");
		const refused = view.messages.find((m) => m.role === "tool" && m.toolCallId === "d1");
		expect(refused).toMatchObject({ isError: true });
		expect(refused?.role === "tool" && refused.text).toContain("isn't one of them");
		expect(tracker.calls).toEqual([]);
		expect(await agent.approvals()).toEqual([]);
	}, 30_000);

	it("writes without asking when approveWrites is off", async () => {
		const tracker = await fakeTracker(ISSUES);
		const model = fakeModel();
		model.reply(toolCall("mcp__tracker__add_comment", { id: 7, body: "Hi" }, { id: "c1" }), "Commented.");
		const settings = JSON.stringify({ ...JSON.parse(SETTINGS), approveWrites: false });
		const agent = track(await startTestAgent(agentForKind("triage", settings), { model, env: tracker.env }));
		await agent.send("t", "Say hi on 7.");
		await agent.settled("t");
		expect(tracker.comments).toEqual([{ id: 7, body: "Hi" }]);
		expect(await agent.approvals()).toEqual([]);
	}, 30_000);
});
