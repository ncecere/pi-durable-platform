import { createServer, type IncomingMessage, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import type { Context as ModelContext } from "@earendil-works/pi-ai";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { AgentDoc } from "@earendil-works/pi-durable";
import { assistantAgent, defineAgent, defineExtension, defineTool, Type } from "@pdp/runtime";
import { answer, fakeModel, startTestAgent, type TestAgent, toolCall } from "@pdp/runtime/testing";
import { afterEach, describe, expect, it } from "vitest";
import { parseConfig } from "../src/lib/standby.ts";

// The schedule tools (agent-made schedules, docs/triggers.md) against a fake gateway: what they send with the agent's
// own credential, what the model reads back, and when the library adds them.

const KEY = "pdpm.agent-test.runtime-token";
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

type Seen = { method: string; path: string; auth: string | undefined; body: Record<string, unknown> | undefined };

/** The platform's gateway: thread reports, and the schedule routes as the platform answers them. */
async function fakeGateway() {
	const seen: Seen[] = [];
	const schedules = new Map<string, Record<string, unknown>>();
	const state = { down: false };
	let next = 1;
	const read = (req: IncomingMessage) =>
		new Promise<string>((resolve) => {
			const parts: Buffer[] = [];
			req.on("data", (c: Buffer) => parts.push(c));
			req.on("end", () => resolve(Buffer.concat(parts).toString("utf8")));
		});
	const server: Server = createServer(async (req, res) => {
		const raw = await read(req);
		const body = raw ? (JSON.parse(raw) as Record<string, unknown>) : undefined;
		const path = req.url ?? "";
		const send = (status: number, value: unknown) => {
			res.writeHead(status, { "content-type": "application/json" });
			res.end(JSON.stringify(value));
		};
		if (path.startsWith("/threads/")) return send(200, { data: { ok: true } });
		if (state.down) return void req.socket.destroy();
		seen.push({ method: req.method ?? "", path, auth: req.headers.authorization, body });
		if (req.method === "POST" && path === "/schedules") {
			if (body?.cron === "* * * * *") {
				return send(400, { error: { code: "too_frequent", message: "A recurring schedule may fire at most once every 5m0s: choose a cron expression whose fires are further apart" } });
			}
			const id = `00000000-0000-0000-0000-00000000000${next++}`;
			const once = typeof body?.at === "string";
			const s = {
				id,
				name: once ? "Reminder" : "Schedule",
				once,
				...(once ? {} : { cron: body?.cron }),
				timezone: body?.timezone,
				message: body?.message,
				threadId: body?.thread === "new" ? null : (body?.threadId ?? null),
				originThreadId: body?.threadId ?? null,
				nextFireAt: "2026-10-21T07:00:00Z",
				paused: false,
			};
			schedules.set(id, s);
			return send(201, { data: { schedule: s, where: s.threadId ? "this" : "new", ...(s.threadId || body?.thread === "new" ? {} : { whyNotHere: "the platform has no record of this conversation" }) } });
		}
		if (req.method === "GET" && path === "/schedules") return send(200, { data: [...schedules.values()] });
		const cancel = /^\/schedules\/(.+)$/.exec(path);
		if (req.method === "DELETE" && cancel) {
			const s = schedules.get(decodeURIComponent(cancel[1]!));
			if (!s) return send(404, { error: { code: "trigger_not_found", message: "Trigger not found" } });
			schedules.delete(s.id as string);
			return send(200, { data: s });
		}
		send(404, { error: { code: "not_found", message: "Not found" } });
	});
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => server.close(resolve)));
	return { url: `http://127.0.0.1:${(server.address() as AddressInfo).port}`, seen, schedules, state };
}

const env = (url: string, allowed = true): Record<string, string> => ({
	PLATFORM_GATEWAY_URL: url,
	PLATFORM_KEY: KEY,
	...(allowed ? { PLATFORM_SCHEDULES: "true" } : {}),
});

/** A model step that keeps what the model was given (its tools, the last message) and answers. */
function capture(into: ModelContext[], reply = "Done.") {
	return async (context: ModelContext) => {
		into.push(structuredClone(context));
		return answer(reply);
	};
}

const lastText = (context: ModelContext) => {
	const last = context.messages.at(-1) as { content: { type: string; text?: string }[] };
	return last.content.map((c) => c.text ?? "").join("");
};

/** The extensions a thread selects, as stored. */
async function selection(agent: TestAgent, threadId: string): Promise<string[]> {
	const record = (await agent.agent.threads.list()).find((t) => t.threadId === threadId);
	const stored = await agent.harness.snapshot(AgentDoc, record!.conversationId as never, BACKGROUND_CONTEXT);
	return [...((stored?.extensions as string[] | undefined) ?? [])];
}

async function start(definition = assistantAgent, environment: Record<string, string>, model = fakeModel()) {
	const agent: TestAgent = await startTestAgent(definition, { model, env: environment });
	cleanup.push(() => agent.close());
	return { agent, model };
}

describe("the schedule tools", () => {
	it("schedule, list and cancel with the agent's credential, naming the thread", async () => {
		const gw = await fakeGateway();
		const seen: ModelContext[] = [];
		const model = fakeModel();
		model.reply(
			toolCall("schedule", { when: "2026-10-21T09:00", timezone: "Europe/Paris", message: "Check the deploy" }, { id: "s1" }),
			capture(seen),
		);
		const { agent } = await start(assistantAgent, env(gw.url), model);
		await agent.send("t1", "Remind me to check the deploy tomorrow at nine");
		await agent.settled("t1");

		expect(await selection(agent, "t1")).toEqual(["assistant", "pdp-schedules"]);
		expect(gw.seen[0]).toEqual({
			method: "POST",
			path: "/schedules",
			auth: `Bearer ${KEY}`,
			body: { at: "2026-10-21T09:00", timezone: "Europe/Paris", message: "Check the deploy", thread: "this", threadId: "t1" },
		});
		expect(lastText(seen[0]!)).toMatch(/Reminder set for 2026-10-21T07:00:00Z \(UTC\)\. It will fire in this conversation\. ID 00000000-0000-0000-0000-000000000001/);

		// A cron expression is recurring work; "new" starts a new conversation each time.
		model.reply(toolCall("schedule", { when: "0 9 * * 1", message: "Weekly summary", thread: "new" }, { id: "s2" }), capture(seen));
		await agent.send("t1", "Every Monday, write a summary");
		await agent.settled("t1", { messages: 4 });
		expect(gw.seen[1]!.body).toEqual({ cron: "0 9 * * 1", timezone: "UTC", message: "Weekly summary", thread: "new", threadId: "t1" });
		expect(lastText(seen[1]!)).toMatch(/Recurring schedule set: cron "0 9 \* \* 1" \(UTC\).*It will fire in a new conversation each time\./);

		model.reply(toolCall("list_schedules", {}, { id: "l1" }), capture(seen));
		await agent.send("t1", "What have you scheduled?");
		await agent.settled("t1", { messages: 6 });
		expect(gw.seen[2]).toMatchObject({ method: "GET", path: "/schedules", auth: `Bearer ${KEY}` });
		expect(lastText(seen[2]!)).toContain('"Check the deploy"');
		expect(lastText(seen[2]!)).toContain("in a new conversation each time");

		model.reply(toolCall("cancel_schedule", { id: "00000000-0000-0000-0000-000000000001" }, { id: "c1" }), capture(seen));
		await agent.send("t1", "Cancel the reminder");
		await agent.settled("t1", { messages: 8 });
		expect(gw.seen[3]).toMatchObject({ method: "DELETE", path: "/schedules/00000000-0000-0000-0000-000000000001" });
		expect(lastText(seen[3]!)).toBe('Cancelled "Reminder".');
		expect([...gw.schedules.keys()]).toEqual(["00000000-0000-0000-0000-000000000002"]);
	});

	it("passes the platform's refusal to the model, as an error", async () => {
		const gw = await fakeGateway();
		const seen: ModelContext[] = [];
		const model = fakeModel();
		model.reply(toolCall("schedule", { when: "* * * * *", message: "Poll" }, { id: "s1" }), capture(seen));
		model.reply(toolCall("cancel_schedule", { id: "nope" }, { id: "c1" }), capture(seen));
		const { agent } = await start(assistantAgent, env(gw.url), model);
		await agent.send("t1", "Poll every minute");
		await agent.settled("t1");
		const result = seen[0]!.messages.at(-1) as { isError?: boolean };
		expect(result.isError).toBe(true);
		expect(lastText(seen[0]!)).toBe("Not scheduled: A recurring schedule may fire at most once every 5m0s: choose a cron expression whose fires are further apart");
		await agent.send("t1", "Cancel nope");
		await agent.settled("t1", { messages: 4 });
		expect(lastText(seen[1]!)).toBe("Not cancelled: Trigger not found");
	});

	it("says the platform can't be reached, without its address", async () => {
		const gw = await fakeGateway();
		gw.state.down = true;
		const seen: ModelContext[] = [];
		const model = fakeModel();
		model.reply(toolCall("list_schedules", {}, { id: "l1" }), capture(seen));
		const { agent } = await start(assistantAgent, env(gw.url), model);
		await agent.send("t1", "List");
		await agent.settled("t1");
		expect(lastText(seen[0]!)).toBe("Couldn't list schedules: The platform could not be reached. Try again later.");
	});

	it("are not there when the version doesn't allow them, or the definition leaves them out", async () => {
		const gw = await fakeGateway();
		for (const [definition, environment] of [
			[assistantAgent, env(gw.url, false)],
			[defineAgent({ ...assistantAgent, schedules: false }), env(gw.url)],
			[assistantAgent, { PLATFORM_SCHEDULES: "true" }], // no gateway to call
		] as const) {
			const seen: ModelContext[] = [];
			const model = fakeModel();
			// A model that calls the tool anyway finds no such tool.
			model.reply(toolCall("schedule", { when: "@daily", message: "x" }, { id: "s1" }), capture(seen));
			const { agent } = await start(definition, environment, model);
			await agent.send("t1", "Hello");
			await agent.settled("t1");
			expect(await selection(agent, "t1")).toEqual(["assistant"]);
			expect((seen[0]!.messages.at(-1) as { isError?: boolean }).isError).toBe(true);
		}
		expect(gw.seen).toEqual([]);
	});

	it("are in a custom agent's threads too, after its own extensions", async () => {
		const gw = await fakeGateway();
		const seen: ModelContext[] = [];
		const model = fakeModel();
		model.reply(toolCall("list_schedules", {}, { id: "l1" }), capture(seen));
		const own = defineTool({ name: "noop", description: "noop", parameters: Type.Object({}), execute: async () => ({ output: [] }) });
		const custom = defineAgent({ name: "custom", extensions: [defineExtension({ name: "acme", tools: [own] })] });
		const { agent } = await start(custom, env(gw.url), model);
		await agent.send("t1", "Hello");
		await agent.settled("t1");
		expect(await selection(agent, "t1")).toEqual(["acme", "pdp-schedules"]);
		expect(gw.seen).toEqual([{ method: "GET", path: "/schedules", auth: `Bearer ${KEY}`, body: undefined }]);
		expect(lastText(seen[0]!)).toBe("You have no schedules.");
	});

	it("are allowed in an activation's environment", () => {
		const config = parseConfig({
			agentId: "a1",
			name: "A",
			instructions: "Hi.",
			schema: "agent_a1",
			databaseUrl: "postgres://x@db/agents",
			token: "0123456789abcdef0123",
			env: { PLATFORM_SCHEDULES: "true", PLATFORM_GATEWAY_URL: "http://gw", PLATFORM_KEY: "k" },
		});
		expect(typeof config === "string" ? config : config.modelEnv.PLATFORM_SCHEDULES).toBe("true");
	});
});
