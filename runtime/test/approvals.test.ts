import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import {
	approvalHook,
	assistantAgent,
	assistantExtension,
	type ChatView,
	defineAgent,
	defineExtension,
	defineSubagent,
	defineTool,
	hook,
	isSecretKey,
	isSecretValue,
	redactArguments,
	runStarter,
	ToolTask,
	toolMatches,
	Type,
} from "@pdp/runtime";
import { expectReplayed, fakeModel, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";
import { applyDelta, type DeltaEvent } from "../src/lib/delta.ts";

const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});
const track = (agent: TestAgent) => {
	cleanup.push(() => agent.close());
	return agent;
};

const marks = (tools: string[], extra: Record<string, unknown> = {}) => ({ TOOL_APPROVALS: JSON.stringify({ tools, ...extra }) });
const ann = { kind: "user", id: "u-ann", name: "Ann" } as const;

/** The tool result of call `id` once the thread settled. */
async function resultOf(agent: TestAgent, threadId: string, id: string) {
	const view = await agent.settled(threadId);
	const result = view.messages.find((m) => m.role === "tool" && m.toolCallId === id);
	if (result?.role !== "tool") throw new Error(`no result for ${id}`);
	return { view, result };
}

function callOf(view: ChatView, id: string) {
	for (const m of view.messages) if (m.role === "assistant") for (const c of m.toolCalls ?? []) if (c.id === id) return c;
	throw new Error(`no call ${id}`);
}

/** A tool that records its runs, with an argument that holds a secret. */
function apiTool() {
	const runs: unknown[] = [];
	const tool = defineTool({
		name: "call_api",
		description: "Call an API",
		parameters: Type.Object({ url: Type.String(), headers: Type.Optional(Type.Record(Type.String(), Type.String())), apiKey: Type.Optional(Type.String()) }),
		execute: async (args) => {
			runs.push(args);
			return { output: [{ type: "text", text: `called ${args.url}` }] };
		},
	});
	const definition = defineAgent({ extensions: ({ instructions }) => [assistantExtension(instructions), defineExtension({ name: "api", tools: [tool] })] });
	return { runs, definition };
}

describe("tool approvals", () => {
	it("a marked tool waits until a person approves it, then runs; the view shows the request and the decision", async () => {
		const model = fakeModel();
		model.reply(toolCall("get_time", {}, { id: "c1" }), "Done.");
		const agent = track(await startTestAgent(assistantAgent, { model, env: marks(["get_*"]) }));
		await agent.send("t", "what time is it?", { author: ann });

		const request = await agent.pendingApproval("t");
		expect(request).toMatchObject({ tool: "get_time", callId: "c1", threadId: "t", status: "pending", requestedFor: ann, arguments: {} });
		expect(request.id).toMatch(/^ap_[A-Za-z0-9_-]{22}$/);
		const waiting = (await agent.view("t"))!;
		expect(waiting.busy).toBe(true);
		expect(waiting.approvals).toEqual([expect.objectContaining({ id: request.id, status: "pending", tool: "get_time", arguments: {} })]);
		expect(callOf(waiting, "c1").approval).toMatchObject({ id: request.id, status: "pending" });
		expect(callOf(waiting, "c1").approval).not.toHaveProperty("arguments");
		// Nothing ran and the model wasn't asked again; the agent says it is waiting for a person.
		expect(model.calls).toBe(1);
		// A reply waiting only for a decision doesn't make its thread busy (contract 2): the agent may hibernate meanwhile.
		expect(await agent.status()).toMatchObject({ busyThreads: 0, waitingThreads: 1, pendingApprovals: 1 });

		expect(await agent.decide(request.id, "approve", { decidedBy: { kind: "user", id: "u-bo", name: "Bo" } })).toBe(200);
		const { view, result } = await resultOf(agent, "t", "c1");
		expect(result.isError).toBe(false);
		expect(result.text).toMatch(/\(UTC\)$/);
		expect(callOf(view, "c1").approval).toMatchObject({ status: "approved", decidedBy: { kind: "user", id: "u-bo", name: "Bo" } });
		expect(view.approvals).toBeUndefined();
		expect(await agent.status()).toMatchObject({ waitingThreads: 0, pendingApprovals: 0 });
		// A second decision changes nothing.
		expect(await agent.decide(request.id, "deny")).toBe(200);
		expect((await agent.approvals("t"))[0]!.status).toBe("approved");
		expect(await agent.decide("ap_unknown", "approve")).toBe(404);
	});

	it("a denied call isn't run and the model gets the refusal with the reason", async () => {
		const model = fakeModel();
		const { runs, definition } = apiTool();
		model.reply(toolCall("call_api", { url: "https://example.com" }, { id: "c1" }), "Understood.");
		const agent = track(await startTestAgent(definition, { model, env: marks(["call_api"]) }));
		await agent.send("t", "call it");
		const request = await agent.pendingApproval("t");
		expect(request.requestedFor).toBeUndefined(); // no author: unattended
		await agent.decide(request.id, "deny", { reason: "Not\ntoday", decidedBy: { kind: "user", id: "u-bo", name: "Bo" } });
		const { view, result } = await resultOf(agent, "t", "c1");
		expect(runs).toEqual([]);
		expect(result.isError).toBe(true);
		expect(result.text).toContain("Tool call blocked: denied by Bo: Not today");
		expect(callOf(view, "c1").approval).toMatchObject({ status: "denied", reason: "Not today" });
	});

	it("tools the template doesn't mark run without asking", async () => {
		const model = fakeModel();
		model.reply(toolCall("get_time", {}, { id: "c1" }), "Done.");
		const agent = track(await startTestAgent(assistantAgent, { model, env: marks(["mcp__github__*"]) }));
		await agent.send("t", "time?");
		const { view, result } = await resultOf(agent, "t", "c1");
		expect(result.isError).toBe(false);
		expect(callOf(view, "c1").approval).toBeUndefined();
		expect(await agent.approvals()).toEqual([]);
	});

	for (const graceful of [false, true]) {
		it(`survives ${graceful ? "hibernation" : "a crash"} while it waits: the call runs once after the decision`, async () => {
			const model = fakeModel();
			const { runs, definition } = apiTool();
			model.reply(toolCall("call_api", { url: "https://example.com/a" }, { id: "c1" }), "Done.");
			const first = await startTestAgent(definition, { model, env: marks(["call_api"]) });
			await first.send("t", "call it", { author: ann });
			const request = await first.pendingApproval("t");

			const second = track(await first.restart({ graceful }));
			const again = await second.pendingApproval("t");
			expect(again).toEqual(request); // the same request, asked once
			expect((await second.approvals()).length).toBe(1);
			expect(await second.status()).toMatchObject({ waitingThreads: 1, pendingApprovals: 1 });
			await second.decide(request.id, "approve");
			const { result } = await resultOf(second, "t", "c1");
			expect(result.text).toBe("called https://example.com/a");
			expect(runs).toHaveLength(1);
			await expectReplayed(second, "t");
		});
	}

	it("a decision made while the agent is down is applied when it starts again", async () => {
		const model = fakeModel();
		const { runs, definition } = apiTool();
		model.reply(toolCall("call_api", { url: "https://example.com" }, { id: "c1" }), "Fine.");
		const first = await startTestAgent(definition, { model, env: marks(["call_api"]) });
		await first.send("t", "call it");
		const request = await first.pendingApproval("t");
		await first.close();
		// A person decides while the agent is hibernated: the platform's gateway answers the start's sync with it.
		const gw = await gateway({ sync: [{ id: request.id, status: "denied", reason: "no", decidedBy: { kind: "user", id: "u", name: "Cy" } }] });
		const second = track(await startTestAgent(definition, { model, storage: first.storage, env: { ...marks(["call_api"]), ...gw.env } }));
		const { result } = await resultOf(second, "t", "c1");
		expect(result.text).toContain("denied by Cy: no");
		expect(runs).toEqual([]);
		const sync = gw.bodies("/approvals/sync")[0] as { approvals: { id: string }[] };
		expect(sync.approvals.map((a) => a.id)).toEqual([request.id]);
	});

	it("times out (without a platform the runtime denies it itself)", async () => {
		const model = fakeModel();
		model.reply(toolCall("get_time", {}, { id: "c1" }), "Too bad.");
		const agent = track(await startTestAgent(assistantAgent, { model, env: marks(["get_time"], { timeoutSeconds: 0.2 }) }));
		await agent.send("t", "time?");
		const request = await agent.pendingApproval("t");
		expect(request.expiresAt! - request.requestedAt).toBe(200);
		const { view, result } = await resultOf(agent, "t", "c1");
		expect(result.text).toContain("Tool call blocked: nobody approved it in time");
		expect(callOf(view, "c1").approval).toMatchObject({ status: "timed_out", decidedBy: { kind: "system" } });
	});

	it("stopping the run cancels the request", async () => {
		const model = fakeModel();
		model.reply(toolCall("get_time", {}, { id: "c1" }));
		const agent = track(await startTestAgent(assistantAgent, { model, env: marks(["get_time"]) }));
		await agent.send("t", "time?");
		const request = await agent.pendingApproval("t");
		const res = await fetch(`${agent.baseUrl}/threads/t/stop`, { method: "POST", headers: { authorization: `Bearer ${agent.token}` } });
		expect(res.status).toBe(202);
		expect((await agent.approvals("t"))[0]).toMatchObject({ id: request.id, status: "cancelled" });
		const view = (await agent.view("t"))!;
		expect(view.busy).toBe(false);
		expect(view.approvals).toBeUndefined();
	});

	it("an author's hook asks through the library, with a summary; one request per call and key", async () => {
		const model = fakeModel();
		const refunds: number[] = [];
		const refund = defineTool({
			name: "refund",
			description: "Refund an amount",
			parameters: Type.Object({ amount: Type.Number() }),
			execute: async ({ amount }) => {
				refunds.push(amount);
				return { output: [{ type: "text", text: `refunded ${amount}` }] };
			},
		});
		const asked: string[] = [];
		const definition = defineAgent({
			extensions: ({ approvals }) => [
				defineExtension({
					name: "refunds",
					tools: [refund],
					hooks: [
						approvalHook(approvals, (call) => (call.name === "refund" && (call.arguments.amount as number) > 100 ? { summary: "A refund over 100" } : undefined)),
						// A second hook asking about the same call with the same key gets the same request.
						hook(ToolTask, {
							beforeTool: async (call, api, context) => {
								if (call.name !== "refund" || (call.arguments.amount as number) <= 100) return undefined;
								const decision = await approvals.request(call, api, context);
								asked.push(decision.status);
								return undefined;
							},
						}),
					],
				}),
			],
		});
		model.reply(toolCall("refund", { amount: 50 }, { id: "small" }), toolCall("refund", { amount: 500 }, { id: "big" }), "Done.");
		const agent = track(await startTestAgent(definition, { model }));
		await agent.send("t", "refund both", { author: ann });
		const request = await agent.pendingApproval("t");
		expect(request).toMatchObject({ callId: "big", summary: "A refund over 100", tool: "refund", arguments: { amount: 500 } });
		await agent.decide(request.id, "approve");
		const { result } = await resultOf(agent, "t", "big");
		expect(result.text).toBe("refunded 500");
		expect(refunds).toEqual([50, 500]);
		expect(asked).toEqual(["approved"]);
		const all = await agent.approvals();
		expect(all.map((a) => a.callId)).toEqual(["big"]);
	});

	it("a call in a subagent's conversation is asked for its thread, and listed in the thread's view", async () => {
		const model = fakeModel();
		const definition = defineAgent({
			extensions: ({ instructions }) => [assistantExtension(instructions), defineSubagent({ name: "delegate", description: "Delegate a task" })],
		});
		// The parent delegates; the child calls get_time, which waits; then the child answers, then the parent.
		model.reply(toolCall("delegate", { task: "what time is it?" }, { id: "d1" }), toolCall("get_time", {}, { id: "child-c1" }), "Noon.", "The subagent says noon.");
		const agent = track(await startTestAgent(definition, { model, env: marks(["get_time"]) }));
		await agent.send("t", "ask the subagent", { author: ann });
		const request = await agent.pendingApproval("t");
		const thread = (await agent.agent.threads.get("t"))!;
		expect(request).toMatchObject({ threadId: "t", callId: "child-c1", requestedFor: ann });
		expect(request.conversationId).not.toBe(thread.id);
		expect((await agent.view("t"))!.approvals?.map((a) => a.id)).toEqual([request.id]);
		await agent.decide(request.id, "approve");
		const view = await agent.settled("t");
		expect(view.messages.at(-1)).toMatchObject({ role: "assistant", text: "The subagent says noon." });
	});

	it("the delta and whole-view streams show the pending request and the decision, in order", async () => {
		const model = fakeModel();
		const agent = track(await startTestAgent(assistantAgent, { model, env: marks(["get_time"]) }));
		await agent.send("t", "hello", { author: ann });
		await agent.settled("t", { messages: 2 });
		model.reply(toolCall("get_time", {}, { id: "c1" }), "Done.");
		const aborter = new AbortController();
		cleanup.push(async () => aborter.abort());
		const deltas = follow(`${agent.baseUrl}/threads/t/events?format=delta&v=1`, agent.token, aborter.signal);
		const views = follow(`${agent.baseUrl}/threads/t/events`, agent.token, aborter.signal);
		await agent.send("t", "what time is it?", { author: ann });
		const request = await agent.pendingApproval("t");
		await until(() => deltas.events.some((e) => e.event === "approvals.set"));
		await agent.decide(request.id, "approve");
		const final = await agent.settled("t");
		await until(() => {
			let view: ChatView | undefined;
			for (const e of deltas.events) view = applyDelta(view, JSON.parse(e.data) as DeltaEvent);
			return JSON.stringify(view) === JSON.stringify(final);
		});
		const events = deltas.events.map((e) => JSON.parse(e.data) as DeltaEvent);
		const set = events.filter((e): e is Extract<DeltaEvent, { type: "approvals.set" }> => e.type === "approvals.set");
		expect(set[0]!.approvals).toEqual([expect.objectContaining({ id: request.id, status: "pending" })]);
		expect(set.at(-1)!.approvals).toEqual([]);
		// The call's own approval: pending, then approved, before its result is added.
		const statuses: string[] = [];
		let resultAt = -1;
		events.forEach((e, i) => {
			if (e.type === "message.updated" || e.type === "message.added") {
				const m = e.message;
				if (m.role === "assistant") for (const c of m.toolCalls ?? []) if (c.approval) statuses.push(c.approval.status);
				if (m.role === "tool" && resultAt < 0) resultAt = i;
			}
		});
		expect(statuses[0]).toBe("pending");
		expect(statuses).toContain("approved");
		const approvedAt = events.findIndex((e) => e.type === "message.updated" && e.message.role === "assistant" && e.message.toolCalls?.[0]?.approval?.status === "approved");
		expect(approvedAt).toBeGreaterThan(-1);
		expect(approvedAt).toBeLessThan(resultAt);
		await until(() => views.events.length > 0 && JSON.stringify(JSON.parse(views.events.at(-1)!.data)) === JSON.stringify(final));
		expect(views.events.some((e) => (JSON.parse(e.data) as ChatView).approvals?.[0]?.id === request.id)).toBe(true);
	});

	it("reports requests to the platform with redacted arguments, and applies a decision the platform already has", async () => {
		const model = fakeModel();
		const { runs, definition } = apiTool();
		const expiresAt = Date.now() + 3_600_000;
		const gw = await gateway({ report: { status: "pending", expiresAt } });
		model.reply(
			toolCall("call_api", { url: "https://example.com", headers: { Authorization: "Bearer s3cret" }, apiKey: "k-123" }, { id: "c1" }),
			"Done.",
		);
		const agent = track(await startTestAgent(definition, { model, env: { ...marks(["call_api"]), ...gw.env } }));
		await agent.send("t", "call it", { author: ann });
		const request = await agent.pendingApproval("t");
		await until(() => gw.bodies("/approvals/report").length > 0);
		const report = gw.bodies("/approvals/report")[0] as Record<string, unknown>;
		expect(report).toMatchObject({ id: request.id, threadId: "t", tool: "call_api", callId: "c1", requestedFor: ann, status: "pending" });
		expect(report.arguments).toEqual({ url: "https://example.com", headers: { Authorization: "[redacted, 13 characters]" }, apiKey: "[redacted, 5 characters]" });
		expect(JSON.stringify(gw.all())).not.toContain("s3cret");
		await until(async () => (await agent.approvals("t"))[0]!.expiresAt === expiresAt);
		// The platform decided before this call asked again (after a restart): its answer is applied at once.
		gw.answer.report = { status: "approved", decidedBy: { kind: "user", id: "u-cy", name: "Cy" } };
		const second = track(await agent.restart());
		const { result } = await resultOf(second, "t", "c1");
		expect(result.text).toBe("called https://example.com");
		expect(runs).toHaveLength(1);
		expect((await second.approvals())[0]).toMatchObject({ status: "approved", decidedBy: { name: "Cy" } });
	});
});

describe("approval helpers", () => {
	it("match tool names and patterns", () => {
		expect(toolMatches("get_time", "get_time")).toBe(true);
		expect(toolMatches("mcp__github__*", "mcp__github__create_issue")).toBe(true);
		expect(toolMatches("mcp__github__*", "mcp__gitlab__x")).toBe(false);
		expect(toolMatches("*", "anything")).toBe(true);
		expect(toolMatches("a.b", "aXb")).toBe(false);
	});

	it("redact credentials and bound the arguments", () => {
		for (const key of ["Authorization", "api_key", "apiKey", "clientSecret", "client_secret", "refresh-token", "access_token", "Cookie", "password", "x-api-key", "private_key", "github_token"]) {
			expect(isSecretKey(key)).toBe(true);
		}
		// Names that only might be: an S3 object's key, an auth method, headers (their own keys are checked), pagination.
		for (const key of ["key", "object_key", "auth", "headers", "signature", "max_tokens", "pageToken", "url", "title", "body", "keyword"]) {
			expect(isSecretKey(key)).toBe(false);
		}
		for (const value of [`pdp_${"A1b2".repeat(11)}`, `pdpat_${"Zz0".repeat(15)}`, `pdpk_${"q".repeat(20)}`, `sk-proj-${"aB3".repeat(16)}`, `ghp_${"a1B2".repeat(9)}`,
			"AKIAIOSFODNN7EXAMPLE", "Bearer abcdefgh12345678", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJl", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"-----BEGIN OPENSSH PRIVATE KEY-----\nb3Blbg==\n-----END OPENSSH PRIVATE KEY-----"]) {
			expect(isSecretValue(value)).toBe(true);
		}
		for (const value of ["reports/2026/10/summary.csv", "550e8400-e29b-41d4-a716-446655440000", "da39a3ee5e6b4b0d3255bfef95601890afd80709", "Fix the login page",
			"token limit exceeded", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", "issue-1234-fix-the-thing-in-the-parser"]) {
			expect(isSecretValue(value)).toBe(false);
		}
		expect(redactArguments({ key: "reports/2026/10/summary.csv", query: "AKIAIOSFODNN7EXAMPLE", password: { a: 1 }, apiKey: "[redacted, 42 characters]" })).toEqual({
			key: "reports/2026/10/summary.csv",
			query: "[redacted, 20 characters]",
			password: "[redacted, 7 characters]",
			apiKey: "[redacted, 42 characters]",
		});
		const long = "x".repeat(5000);
		const view = redactArguments({ nested: { token: "t", deep: { a: { b: { c: { d: { e: 1 } } } } } }, long, list: Array.from({ length: 150 }, (_, i) => i) }) as Record<string, any>;
		expect(view.nested.token).toBe("[redacted, 1 character]");
		expect(view.long).toHaveLength(2001);
		expect(view.list).toHaveLength(101);
		expect(JSON.stringify(view)).toContain("[…]");
		const huge = redactArguments(Object.fromEntries(Array.from({ length: 90 }, (_, i) => [`k${i}`, "y".repeat(1000)]))) as Record<string, string>;
		expect(Object.keys(huge)).toEqual(["[truncated]"]);
		expect(huge["[truncated]"]!.length).toBeLessThanOrEqual(2000);
	});

	it("find who a run is for: the message that started it, not a steer that joined it", () => {
		const user = (id: number, name?: string) => ({ id, kind: "pdp.user", data: { text: "x", at: 1, ...(name ? { author: { kind: "user", id: name, name } } : {}) } });
		const assistant = (id: number, stopReason: string) => ({ id, kind: "pi.assistant", model: [{ role: "assistant", content: [], stopReason }] });
		const entries = [user(1, "old"), assistant(2, "stop"), user(3, "ann"), assistant(4, "toolUse"), user(5, "bo"), assistant(6, "toolUse")] as never[];
		expect(runStarter(entries)?.name).toBe("ann");
		expect(runStarter([user(1)] as never[])).toBeUndefined();
	});
});

/** A fake platform gateway: records what the runtime posts and answers approvals as told. */
async function gateway(answer: { report?: Record<string, unknown>; sync?: unknown[] }) {
	const received: { path: string; body: unknown }[] = [];
	const state = { report: answer.report ?? { status: "pending" }, sync: answer.sync ?? [] };
	const server: Server = createServer((req: IncomingMessage, res: ServerResponse) => {
		let raw = "";
		req.on("data", (c: Buffer) => (raw += c.toString()));
		req.on("end", () => {
			const path = req.url ?? "/";
			const body = raw === "" ? undefined : JSON.parse(raw);
			received.push({ path, body });
			const id = (body as { id?: string } | undefined)?.id;
			const data = path === "/approvals/report" ? { id, ...state.report } : path === "/approvals/sync" ? state.sync : { ok: true };
			res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ data }));
		});
	});
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => server.close(resolve)));
	const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
	return {
		env: { PLATFORM_GATEWAY_URL: url, PLATFORM_KEY: "agent-key" },
		answer: state,
		bodies: (path: string) => received.filter((r) => r.path === path).map((r) => r.body),
		all: () => received,
	};
}

async function until(check: () => boolean | Promise<boolean>, ms = 10_000): Promise<void> {
	const deadline = Date.now() + ms;
	while (!(await check())) {
		if (Date.now() > deadline) throw new Error("timed out waiting");
		await new Promise((r) => setTimeout(r, 20));
	}
}

/** Reads server-sent events until `signal` aborts the stream. */
function follow(url: string, token: string, signal: AbortSignal) {
	const events: { event: string; data: string }[] = [];
	void (async () => {
		const decoder = new TextDecoder();
		let buffer = "";
		try {
			const res = await fetch(url, { headers: { authorization: `Bearer ${token}` }, signal });
			for await (const chunk of res.body as unknown as AsyncIterable<Uint8Array>) {
				buffer += decoder.decode(chunk, { stream: true });
				let cut: number;
				while ((cut = buffer.indexOf("\n\n")) >= 0) {
					const block = buffer.slice(0, cut);
					buffer = buffer.slice(cut + 2);
					if (block.startsWith(":")) continue;
					const field = (name: string) => block.split("\n").find((l) => l.startsWith(`${name}: `))?.slice(name.length + 2);
					events.push({ event: field("event")!, data: field("data")! });
				}
			}
		} catch {
			// aborted
		}
	})();
	return { events };
}
