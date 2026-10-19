import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { startAgent } from "../src/agent.ts";
import { dropSchema, uniqueSchema, withAdmin, ADMIN_URL } from "./support.ts";

const TOKEN = "runtime-token-0123456789";
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

type Status = { busyThreads: number; openStreams: number; lastActivityAt: number };

const status = async (base: string): Promise<Status> =>
	((await (await fetch(`${base}/status`, { headers: { authorization: `Bearer ${TOKEN}` } })).json()) as { data: Status }).data;

async function until<T>(read: () => Promise<T>, ok: (v: T) => boolean): Promise<T> {
	for (let i = 0; i < 100; i++) {
		const v = await read();
		if (ok(v)) return v;
		await new Promise((r) => setTimeout(r, 50));
	}
	throw new Error("condition not met");
}

// Several platform replicas share no memory: the reaper of one asks the runtime, which serves every stream, whether
// someone is watching.
describe("/status reports the open event streams", () => {
	it("counts streams while they are open and starts the idle clock when they close", async () => {
		const schema = uniqueSchema("agent_st");
		await withAdmin((admin) => admin.query(`CREATE SCHEMA "${schema}"`));
		cleanup.push(() => dropSchema(schema));
		const agent = await startAgent({
			agentId: "agent-st",
			name: "Status",
			instructions: "Be brief.",
			schema,
			databaseUrl: ADMIN_URL,
			token: TOKEN,
			modelEnv: { MODEL_PROVIDER: "demo", DEMO_TOKENS_PER_SECOND: "100000" },
		});
		cleanup.push(() => agent.close());
		const server: Server = createServer(agent.handler);
		await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
		cleanup.push(() => new Promise((resolve) => server.close(resolve)));
		const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
		const headers = { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" };

		expect((await status(base)).openStreams).toBe(0);
		const sent = await fetch(`${base}/threads/t1/messages`, { method: "POST", headers, body: JSON.stringify({ text: "hello", requestId: "m1" }) });
		expect(sent.status).toBe(202);

		// A one-shot read is not a stream.
		await fetch(`${base}/threads/t1/events?once=true`, { headers }).then((r) => r.text());
		expect((await status(base)).openStreams).toBe(0);

		const first = new AbortController();
		const second = new AbortController();
		const open = async (signal: AbortSignal) => {
			const res = await fetch(`${base}/threads/t1/events`, { headers, signal });
			expect(res.status).toBe(200);
			const reader = res.body!.getReader();
			await reader.read(); // the first view: the stream is established
		};
		await open(first.signal);
		await open(second.signal);
		expect((await until(() => status(base), (s) => s.openStreams === 2)).openStreams).toBe(2);

		first.abort();
		await until(() => status(base), (s) => s.openStreams === 1);
		await new Promise((r) => setTimeout(r, 200));
		const mid = await status(base);
		expect(mid.openStreams).toBe(1);

		// Closing the last stream counts as activity: the agent is not idle until the idle timeout after it.
		const before = Date.now();
		second.abort();
		const done = await until(() => status(base), (s) => s.openStreams === 0);
		expect(done.lastActivityAt).toBeGreaterThanOrEqual(before - 5);
	});
});
