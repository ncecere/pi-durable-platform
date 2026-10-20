import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import type { EntryRecord } from "@earendil-works/pi-durable";
import { afterEach, describe, expect, it } from "vitest";
import { startAgent } from "../src/lib/agent.ts";
import { buildView } from "../src/lib/view.ts";
import { ADMIN_URL, dropSchema, uniqueSchema, withAdmin } from "./support.ts";

const TOKEN = "runtime-token-0123456789";
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

async function listen(server: Server): Promise<string> {
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => server.close(resolve)));
	return `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
}

/** A chat-completions server that streams a fixed reply and keeps each request's messages. */
async function recordingModel(reply: string): Promise<{ baseUrl: string; requests: { role: string; content: unknown }[][] }> {
	const requests: { role: string; content: unknown }[][] = [];
	const chunk = (delta: object, finish: string | null, usage?: object) =>
		`data: ${JSON.stringify({ id: "c1", object: "chat.completion.chunk", created: 0, model: "fake-1", choices: [{ index: 0, delta, finish_reason: finish }], ...(usage ? { usage } : {}) })}\n\n`;
	const server = createServer((req, res) => {
		const parts: Buffer[] = [];
		req.on("data", (c: Buffer) => parts.push(c));
		req.on("end", () => {
			requests.push((JSON.parse(Buffer.concat(parts).toString("utf8")) as { messages: { role: string; content: unknown }[] }).messages);
			res.writeHead(200, { "content-type": "text/event-stream" });
			res.write(chunk({ role: "assistant", content: reply }, null));
			res.write(chunk({}, "stop", { prompt_tokens: 7, completion_tokens: 3, total_tokens: 10 }));
			res.end("data: [DONE]\n\n");
		});
	});
	return { baseUrl: `${await listen(server)}/v1/`, requests };
}

const modelEnv = (baseUrl: string) => ({
	MODEL_PROVIDER: "catalog",
	MODEL_API: "openai-completions",
	MODEL_BASE_URL: baseUrl,
	MODEL_API_KEY: "test-key",
	MODEL_ID: "fake-1",
	MODEL_NAME: "Fake",
	MODEL_PROVIDER_NAME: "Fake",
});

async function runAgent(schema: string, env: Record<string, string>) {
	const agent = await startAgent({ agentId: "agent-au", name: "Authors", instructions: "Be brief.", schema, databaseUrl: ADMIN_URL, token: TOKEN, modelEnv: env });
	const base = await listen(createServer(agent.handler));
	return { agent, base };
}

type Msg = {
	role: string;
	text?: string;
	error?: string;
	at?: number;
	author?: { kind: string; id: string; name: string; via?: string };
	model?: { provider: string; id: string };
	usage?: { input: number; output: number; cacheRead: number; cacheWrite: number; total: number };
	stopReason?: string;
};

async function send(base: string, threadId: string, body: object): Promise<void> {
	const res = await fetch(`${base}/threads/${threadId}/messages`, {
		method: "POST",
		headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" },
		body: JSON.stringify(body),
	});
	expect(res.status).toBe(202);
}

/** The thread's messages once it has n of them and is idle. */
async function messages(base: string, threadId: string, n: number): Promise<Msg[]> {
	for (let i = 0; i < 100; i++) {
		const res = await fetch(`${base}/threads/${threadId}`, { headers: { authorization: `Bearer ${TOKEN}` } });
		if (res.status === 200) {
			const view = ((await res.json()) as { data: { busy: boolean; messages: Msg[] } }).data;
			if (!view.busy && view.messages.length >= n && view.messages.at(-1)?.role === "assistant") return view.messages;
		}
		await new Promise((r) => setTimeout(r, 100));
	}
	throw new Error("no reply");
}

const userTexts = (request: { role: string; content: unknown }[]) =>
	request.filter((m) => m.role === "user").map((m) => (typeof m.content === "string" ? m.content : JSON.stringify(m.content)));

describe("message authors", () => {
	it("records who sent a message, tells the model on shared threads, and survives a restart", async () => {
		const schema = uniqueSchema("agent_au");
		await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`));
		cleanup.push(() => dropSchema(schema));
		const model = await recordingModel("Hello Alex");
		const alex = { kind: "user", id: "4c1a1f7e-0000-4000-8000-000000000001", name: "Alex Dev" };
		const blair = { kind: "user", id: "4c1a1f7e-0000-4000-8000-000000000002", name: "Blair\nInjected]", via: "Chat app" };

		const first = await runAgent(schema, modelEnv(model.baseUrl));
		const before = Date.now();
		await send(first.base, "t1", { text: "hello there", author: alex, announceAuthor: true });
		let view = await messages(first.base, "t1", 2);
		// The model was told who is speaking; the transcript shows the text as sent.
		expect(userTexts(model.requests[0]!).at(-1)).toContain("[Message from Alex Dev]\nhello there");
		expect(view[0]).toMatchObject({ role: "user", text: "hello there", author: alex });
		expect(view[0]!.at).toBeGreaterThanOrEqual(before);
		expect(view[1]).toMatchObject({ role: "assistant", text: "Hello Alex", stopReason: "stop", model: { id: "fake-1" } });
		expect(view[1]!.model!.provider).toBeTypeOf("string");
		expect(view[1]!.usage).toMatchObject({ input: 7, output: 3, total: 10 });
		expect(view[1]!.at).toBeGreaterThanOrEqual(before);

		// Without announceAuthor (private threads, personal workspaces) the model gets the text alone.
		await send(first.base, "t2", { text: "just me", author: alex, announceAuthor: false });
		view = await messages(first.base, "t2", 2);
		expect(userTexts(model.requests[1]!).at(-1)).toBe("just me");
		expect(view[0]).toMatchObject({ text: "just me", author: alex });
		await first.agent.close();

		// After a restart the conversation, its authors included, is still there and answered.
		const second = await runAgent(schema, modelEnv(model.baseUrl));
		cleanup.push(() => second.agent.close());
		await send(second.base, "t1", { text: "again", author: blair, announceAuthor: true });
		view = await messages(second.base, "t1", 4);
		const sent = userTexts(model.requests[2]!);
		expect(sent).toHaveLength(2);
		expect(sent[0]).toContain("[Message from Alex Dev]\nhello there");
		// A name can't break out of its line.
		expect(sent[1]).toContain("[Message from Blair Injected] via Chat app]\nagain");
		expect(view.map((m) => m.role)).toEqual(["user", "assistant", "user", "assistant"]);
		expect(view[0]).toMatchObject({ text: "hello there", author: alex });
		expect(view[2]).toMatchObject({ text: "again", author: { name: "Blair Injected]", via: "Chat app" } });
	});
});

describe("the chat view", () => {
	it("renders entries without author data, and times tool calls", () => {
		const entry = (id: number, kind: string, model: unknown[], data?: unknown) =>
			({ id, conversationId: 1, kind, model, ...(data === undefined ? {} : { data }) }) as unknown as EntryRecord;
		const usage = { input: 1, output: 2, cacheRead: 3, cacheWrite: 4, totalTokens: 10, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } };
		const view = buildView(
			[
				entry(1, "pi.user", [{ role: "user", content: "old message", timestamp: 1000 }]),
				entry(2, "pi.user", [{ role: "user", content: [{ type: "text", text: "no time" }] }]),
				entry(3, "pdp.user", [{ role: "user", content: "[Message from A]\nhi", timestamp: 2000 }], { text: "hi", at: 1999 }),
				entry(4, "pi.assistant", [
					{
						role: "assistant",
						content: [{ type: "toolCall", id: "call1", name: "get_time", arguments: {} }],
						api: "x",
						provider: "p",
						model: "m",
						usage,
						stopReason: "toolUse",
						timestamp: 3000,
						durationMs: 50,
					},
				]),
				entry(5, "pi.tool", [{ role: "toolResult", toolCallId: "call1", toolName: "get_time", content: [{ type: "text", text: "noon" }], isError: false, timestamp: 3200 }]),
			],
			undefined,
		);
		expect(view.messages[0]).toEqual({ id: "e1", role: "user", text: "old message", at: 1000 });
		expect(view.messages[1]).toEqual({ id: "e2", role: "user", text: "no time" });
		expect(view.messages[2]).toEqual({ id: "e3", role: "user", text: "hi", at: 1999 });
		expect(view.messages[3]).toMatchObject({
			role: "assistant",
			at: 3000,
			model: { provider: "p", id: "m" },
			usage: { input: 1, output: 2, cacheRead: 3, cacheWrite: 4, total: 10 },
			stopReason: "toolUse",
			toolCalls: [{ id: "call1", name: "get_time", arguments: {}, startedAt: 3050, endedAt: 3200 }],
		});
		expect(view.messages[4]).toMatchObject({ role: "tool", toolCallId: "call1", text: "noon", at: 3200 });
	});
});
