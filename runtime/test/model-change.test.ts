import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { startAgent } from "../src/lib/agent.ts";
import { unansweredError } from "../src/lib/thread-status.ts";
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

/** A chat-completions server that streams one fixed reply. */
async function fakeModelServer(reply: string): Promise<string> {
	const chunk = (delta: object, finish: string | null, usage?: object) =>
		`data: ${JSON.stringify({ id: "c1", object: "chat.completion.chunk", created: 0, model: "fake-1", choices: [{ index: 0, delta, finish_reason: finish }], ...(usage ? { usage } : {}) })}\n\n`;
	const server = createServer((req, res) => {
		req.resume();
		req.on("end", () => {
			res.writeHead(200, { "content-type": "text/event-stream" });
			res.write(chunk({ role: "assistant", content: reply }, null));
			res.write(chunk({}, "stop", { prompt_tokens: 3, completion_tokens: 2, total_tokens: 5 }));
			res.end("data: [DONE]\n\n");
		});
	});
	return `${await listen(server)}/v1/`;
}

async function runAgent(schema: string, modelEnv: Record<string, string>) {
	const agent = await startAgent({ agentId: "agent-mc", name: "Model change", instructions: "Be brief.", schema, databaseUrl: ADMIN_URL, token: TOKEN, modelEnv });
	const base = await listen(createServer(agent.handler));
	return { agent, base };
}

type View = { messages: { role: string; text?: string; error?: string }[] };

async function send(base: string, threadId: string, text: string): Promise<void> {
	const res = await fetch(`${base}/threads/${threadId}/messages`, {
		method: "POST",
		headers: { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" },
		body: JSON.stringify({ text }),
	});
	expect(res.status).toBe(202);
}

/** Waits until the thread's last message is an assistant reply. */
async function lastReply(base: string, threadId: string): Promise<{ role: string; text?: string; error?: string }> {
	for (let i = 0; i < 100; i++) {
		const res = await fetch(`${base}/threads/${threadId}/events?once=true`, { headers: { authorization: `Bearer ${TOKEN}` } });
		const body = await res.text();
		const data = body.split("\n").find((l) => l.startsWith("data: "));
		const view = data ? (JSON.parse(data.slice(6)) as View & { run?: unknown }) : undefined;
		const last = view?.messages.at(-1);
		if (last?.role === "assistant" && view?.run === undefined) return last;
		await new Promise((r) => setTimeout(r, 100));
	}
	throw new Error("no reply");
}

describe("an agent's model changes", () => {
	it("moves a thread begun on the old model to the new one", async () => {
		const schema = uniqueSchema("agent_mc");
		await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`));
		cleanup.push(() => dropSchema(schema));

		const first = await runAgent(schema, { MODEL_PROVIDER: "demo", DEMO_TOKENS_PER_SECOND: "100000" });
		await send(first.base, "t1", "hello");
		expect((await lastReply(first.base, "t1")).error).toBeUndefined();
		await first.agent.close();

		const baseUrl = await fakeModelServer("from the new model");
		const second = await runAgent(schema, {
			MODEL_PROVIDER: "catalog",
			MODEL_API: "openai-completions",
			MODEL_BASE_URL: baseUrl,
			MODEL_API_KEY: "test-key",
			MODEL_ID: "fake-1",
			MODEL_NAME: "Fake",
			MODEL_PROVIDER_NAME: "Fake",
		});
		cleanup.push(() => second.agent.close());
		await send(second.base, "t1", "still there?");
		const reply = await lastReply(second.base, "t1");
		expect(reply.error).toBeUndefined();
		expect(reply.text).toBe("from the new model");
	});
});

describe("unansweredError", () => {
	it("explains why a message got no reply, except a stop", () => {
		expect(unansweredError("aborted", undefined)).toBeUndefined();
		expect(unansweredError("no_model", undefined)).toMatch(/model isn't available/);
		expect(unansweredError("model_error", "upstream 500")).toBe("No reply: the model request failed: upstream 500");
		expect(unansweredError("something_new", undefined)).toBe("No reply (something_new)");
	});
});
