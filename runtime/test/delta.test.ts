import type { AddressInfo } from "node:net";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type Message, Type } from "@earendil-works/pi-ai";
import { createModels } from "@earendil-works/pi-ai/models";
import { fauxAssistantMessage, fauxProvider, fauxText, fauxToolCall } from "@earendil-works/pi-ai/providers/faux";
import { createRegistry, defineExtension, defineTool, Harness, MemoryStorage } from "@earendil-works/pi-durable";
import { afterEach, describe, expect, it } from "vitest";
import { applyDelta, type DeltaEvent, DeltaStream } from "../src/delta.ts";
import { createRuntimeServer } from "../src/server.ts";
import { ThreadStore } from "../src/threads.ts";
import { type ChatMessage, type ChatTool, type ChatView, cursorMessage, messageCursor, pageView, parsePage } from "../src/view.ts";

const ctx = BACKGROUND_CONTEXT;
const TOKEN = "runtime-token-0123456789";
const cleanup: (() => Promise<unknown>)[] = [];
afterEach(async () => {
	while (cleanup.length > 0) await cleanup.pop()!().catch(() => {});
});

/** A small deterministic PRNG (mulberry32), so a failing case can be replayed from its seed. */
function rng(seed: number) {
	let a = seed >>> 0;
	const next = () => {
		a = (a + 0x6d2b79f5) >>> 0;
		let t = a;
		t = Math.imul(t ^ (t >>> 15), t | 1);
		t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
	return { next, int: (n: number) => Math.floor(next() * n), pick: <T>(xs: readonly T[]) => xs[Math.floor(next() * xs.length)]! };
}

const words = ["alpha ", "beta ", "gamma\n", "δέλτα ", "🙂 ", "x", "\n\n", '"quoted" '];

/** The next view of a random evolution: what a conversation's commits do to the chat view. */
function evolve(view: ChatView, r: ReturnType<typeof rng>, ids: { next: number }): ChatView {
	const v: ChatView = structuredClone(view);
	switch (r.int(12)) {
		case 0:
		case 1:
		case 2: {
			// Tokens streamed into the reply in progress.
			const p = v.partial ?? { text: "", thinking: "" };
			v.partial = r.int(4) === 0 ? { ...p, thinking: p.thinking + r.pick(words) } : { ...p, text: p.text + r.pick(words) + r.pick(words) };
			v.busy = true;
			break;
		}
		case 3: {
			// The reply ends: added to the transcript, with tool calls sometimes.
			const id = `e${ids.next++}`;
			const calls = r.int(2) === 0 ? [{ id: `call${ids.next}`, name: "get_time", arguments: {}, startedAt: 1000 }] : undefined;
			v.messages.push({ id, role: "assistant", text: v.partial?.text ?? "done", ...(calls ? { toolCalls: calls } : {}) });
			delete v.partial;
			if (calls) v.tools = [{ callId: calls[0]!.id, name: "get_time", status: "pending" }];
			break;
		}
		case 4:
			v.messages.push({ id: `e${ids.next++}`, role: "user", text: r.pick(words), at: 5 });
			v.busy = true;
			break;
		case 5: {
			// A tool runs and writes output.
			const t = v.tools.at(-1);
			if (t === undefined) v.tools.push({ callId: `c${ids.next++}`, name: "slow", status: "running" });
			else if (t.status !== "done") {
				t.status = "running";
				t.output = r.int(5) === 0 ? r.pick(words) : (t.output ?? "") + r.pick(words);
			}
			break;
		}
		case 6: {
			// A tool ends: its result is added, its call timed (the call's message changes).
			const t = v.tools.find((x) => x.status !== "done");
			if (t !== undefined) {
				t.status = "done";
				delete t.output;
				v.messages.push({ id: `e${ids.next++}`, role: "tool", toolCallId: t.callId, name: t.name, text: "ok", isError: false });
				for (const m of v.messages) if (m.role === "assistant") for (const c of m.toolCalls ?? []) if (c.id === t.callId) c.endedAt = 2000;
			}
			break;
		}
		case 7:
			v.tools = [];
			break;
		case 8:
			v.busy = !v.busy;
			break;
		case 9:
			// A retry starts the reply over.
			if (v.partial) v.partial = { text: r.pick(words), thinking: "" };
			break;
		case 10:
			// Compaction or reset: the transcript loses its start.
			if (r.int(4) === 0) v.messages = v.messages.slice(Math.min(v.messages.length, 1 + r.int(3)));
			break;
		case 11:
			if (v.tools.length > 1) v.tools.reverse();
			else if (v.tools.length === 1) v.tools.push({ callId: `c${ids.next++}`, name: "two", status: "pending" });
			break;
	}
	return v;
}

/** What a client following a windowed stream holds: the view from its window's first message on. */
function windowOf(view: ChatView, client: ChatView): ChatView {
	const from = client.messages.length === 0 ? view.messages.length : view.messages.findIndex((m) => m.id === client.messages[0]!.id);
	const { earlierCursor: _, ...rest } = view;
	return { ...rest, messages: view.messages.slice(from), ...(client.earlierCursor ? { earlierCursor: client.earlierCursor } : {}) };
}

describe("delta events", () => {
	it("applied in order, give the view a snapshot would (random evolutions)", () => {
		for (let seed = 1; seed <= 400; seed++) {
			const r = rng(seed);
			const limit = seed % 3 === 0 ? 1 + r.int(4) : undefined;
			const stream = new DeltaStream(limit, 0, () => 10_000);
			const ids = { next: 1 };
			let view: ChatView = { busy: false, messages: [], tools: [] };
			let client: ChatView | undefined;
			let seq = 0;
			for (let step = 0; step < 60; step++) {
				// Coalescing: several changes may land between two flushes.
				for (let k = 1 + r.int(3); k > 0; k--) view = evolve(view, r, ids);
				for (const event of stream.next(view, true)) {
					expect(event.seq, `seed ${seed}`).toBe(++seq);
					client = applyDelta(client, JSON.parse(JSON.stringify(event)) as DeltaEvent);
				}
				const want = limit === undefined ? view : windowOf(view, client!);
				expect(client, `seed ${seed} step ${step}`).toEqual(want);
			}
		}
	}, 60_000); // 400 seeded runs: about a second here, several on a slow CI runner; the default 5 s timeout is for hangs

	it("send bytes that follow the new tokens, not the transcript's length", () => {
		const transcript = (n: number): ChatMessage[] =>
			Array.from({ length: n }, (_, i) => ({ id: `e${i + 1}`, role: i % 2 ? "assistant" : "user", text: "lorem ipsum ".repeat(80) }) as ChatMessage);
		/** Bytes of the events (after the snapshot) and of whole views, while `tokens` tokens stream into a reply. */
		const traffic = (messages: number, tokens: number) => {
			const stream = new DeltaStream(undefined);
			let view: ChatView = { busy: true, messages: transcript(messages), tools: [] };
			stream.next(view);
			let deltas = 0;
			let whole = 0;
			for (let i = 0; i < tokens; i++) {
				view = { ...view, partial: { text: `${view.partial?.text ?? ""}tok${i % 10} `, thinking: "" } };
				for (const e of stream.next(view, false)) deltas += Buffer.byteLength(`id: ${e.seq}\nevent: ${e.type}\ndata: ${JSON.stringify(e)}\n\n`);
				whole += Buffer.byteLength(`event: view\ndata: ${JSON.stringify(view)}\n\n`);
			}
			return { deltas, whole };
		};
		const short = traffic(2, 200);
		const long = traffic(400, 200);
		// The transcript's length doesn't matter (400 messages of ~1 KB each against 2)...
		expect(long.deltas).toBe(short.deltas);
		expect(long.whole).toBeGreaterThan(100 * long.deltas);
		// ...and the bytes grow linearly with the tokens.
		const double = traffic(400, 400);
		expect(double.deltas / long.deltas).toBeGreaterThan(1.9);
		expect(double.deltas / long.deltas).toBeLessThan(2.1);
		// Each token costs its text plus a small, fixed envelope.
		expect(long.deltas / 200).toBeLessThan(100);
	});

	it("times tool calls: from the transcript's call, else from when the stream first saw them", () => {
		let now = 5_000;
		const stream = new DeltaStream(undefined, 41, () => now);
		const call = { id: "c1", name: "slow", arguments: {}, startedAt: 4_000 };
		const tool: ChatTool = { callId: "c1", name: "slow", status: "running" };
		stream.next({ busy: true, messages: [{ id: "e1", role: "assistant", text: "", toolCalls: [call] }], tools: [] });
		now = 6_500;
		const [started] = stream.next({ busy: true, messages: [{ id: "e1", role: "assistant", text: "", toolCalls: [call] }], tools: [tool] }, false);
		expect(started).toMatchObject({ seq: 43, type: "tool.started", startedAt: 4_000, elapsedMs: 2_500 });
		now = 7_000;
		const [unseen] = stream.next({ busy: true, messages: [{ id: "e1", role: "assistant", text: "", toolCalls: [call] }], tools: [tool, { callId: "c2", name: "x", status: "running" }] }, false);
		expect(unseen).toMatchObject({ type: "tool.started", startedAt: 7_000, elapsedMs: 0 });
	});
});

describe("message pages", () => {
	const view: ChatView = {
		busy: false,
		tools: [],
		messages: [
			{ id: "e1", role: "user", text: "1" },
			{ id: "e2", role: "assistant", text: "", toolCalls: [{ id: "c", name: "t", arguments: {} }] },
			{ id: "e3", role: "tool", toolCallId: "c", name: "t", text: "", isError: false },
			{ id: "e3:1", role: "tool", toolCallId: "d", name: "t", text: "", isError: false },
			{ id: "e4", role: "assistant", text: "4" },
			{ id: "e5", role: "user", text: "5" },
		],
	};
	it("page backwards with opaque cursors, never starting with a tool result", () => {
		const first = pageView(view, { limit: 2 });
		expect(first.messages.map((m) => m.id)).toEqual(["e4", "e5"]);
		const second = pageView(view, { limit: 2, before: cursorMessage(first.earlierCursor!)! });
		// e3:1 and e3 are tool results: the page starts at the reply that called them.
		expect(second.messages.map((m) => m.id)).toEqual(["e2", "e3", "e3:1"]);
		const third = pageView(view, { limit: 2, before: cursorMessage(second.earlierCursor!)! });
		expect(third.messages.map((m) => m.id)).toEqual(["e1"]);
		expect(third.earlierCursor).toBeUndefined();
		expect(pageView(view, { limit: 50 }).earlierCursor).toBeUndefined();
		// A message no longer in the view (compacted): an empty page.
		expect(pageView(view, { limit: 2, before: "e99" }).messages).toEqual([]);
	});
	it("reject malformed cursors and limits", () => {
		expect(parsePage(new URLSearchParams())).toEqual({});
		expect(parsePage(new URLSearchParams("limit=0")).error?.code).toBe("invalid_limit");
		expect(parsePage(new URLSearchParams("limit=201")).error?.code).toBe("invalid_limit");
		expect(parsePage(new URLSearchParams("limit=2x")).error?.code).toBe("invalid_limit");
		expect(parsePage(new URLSearchParams("before=e4")).error?.code).toBe("invalid_cursor");
		expect(parsePage(new URLSearchParams(`before=${Buffer.from("m:x1").toString("base64url")}`)).error?.code).toBe("invalid_cursor");
		expect(parsePage(new URLSearchParams(`before=${messageCursor("e4")}`)).page).toEqual({ before: "e4", limit: 50 });
	});
});

// A conversation with a scripted model that streams slowly, and a slow tool the test releases.
async function runtime() {
	let release: () => void = () => {};
	let toolStarted: () => void = () => {};
	const started = new Promise<void>((resolve) => (toolStarted = resolve));
	const slow = defineTool({
		name: "slow_lookup",
		description: "Looks something up, slowly.",
		parameters: Type.Object({}),
		replay: "safe",
		execute: async (_args, api) => {
			api.output("looking");
			toolStarted();
			await new Promise<void>((resolve) => (release = resolve));
			api.output(" up");
			await new Promise((resolve) => setTimeout(resolve, 300));
			return { output: [{ type: "text", text: "found it" }] };
		},
	});
	const faux = fauxProvider({ provider: "test", models: [{ id: "slow", name: "slow" }], tokensPerSecond: 80, tokenSize: { min: 1, max: 2 } });
	const userTexts = (messages: readonly Message[]) =>
		messages.flatMap((m) => (m.role === "user" ? [typeof m.content === "string" ? m.content : m.content.map((c) => (c.type === "text" ? c.text : "")).join("")] : []));
	const respond = (context: { messages: readonly Message[] }) => {
		faux.appendResponses([respond]);
		const last = context.messages.at(-1)!;
		const users = userTexts(context.messages);
		if (last.role === "user" && users.length === 1) return fauxAssistantMessage([fauxText("Let me look that up. "), fauxToolCall("slow_lookup", {})], { stopReason: "toolUse" });
		// The reply says which of the user's messages it has seen.
		return fauxAssistantMessage([fauxText(`Answer to: ${users.join(" | ")}. ${"More words follow here. ".repeat(6)}`)]);
	};
	faux.setResponses([respond]);
	const models = createModels();
	models.setProvider(faux.provider);
	const extension = defineExtension({ name: "test", tools: [slow] });
	const registry = createRegistry();
	registry.install(extension);
	const harness = await Harness.open(new MemoryStorage(), { models, registry }, ctx);
	harness.resume();
	cleanup.push(() => harness.close(ctx));
	const threads = new ThreadStore(harness, () => ({ model: { provider: "test", modelId: "slow" }, extensions: [extension] }));
	const { server } = createRuntimeServer({ harness, threads, token: TOKEN, info: { agentId: "a", name: "A", model: "test/slow" } });
	await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
	cleanup.push(() => new Promise((resolve) => server.close(resolve)));
	const base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
	return { base, started, release: () => release() };
}

const headers = { authorization: `Bearer ${TOKEN}`, "content-type": "application/json" };

async function send(base: string, body: object): Promise<Response> {
	return fetch(`${base}/threads/t1/messages`, { method: "POST", headers, body: JSON.stringify(body) });
}

async function getView(base: string, query = ""): Promise<ChatView> {
	const res = await fetch(`${base}/threads/t1${query}`, { headers });
	expect(res.status).toBe(200);
	return ((await res.json()) as { data: ChatView }).data;
}

async function idle(base: string, messages: number): Promise<ChatView> {
	for (let i = 0; i < 300; i++) {
		const view = await getView(base);
		if (!view.busy && view.messages.length >= messages && view.partial === undefined) return view;
		await new Promise((r) => setTimeout(r, 50));
	}
	throw new Error("the reply didn't end");
}

/** Reads server-sent events into `events` until the stream ends or `signal` aborts it. */
function follow(url: string, signal: AbortSignal, init: { lastEventId?: string } = {}) {
	const events: { id?: string; event: string; data: string }[] = [];
	let bytes = 0;
	const done = (async () => {
		const res = await fetch(url, { headers: { ...headers, ...(init.lastEventId ? { "last-event-id": init.lastEventId } : {}) }, signal });
		expect(res.status).toBe(200);
		const decoder = new TextDecoder();
		let buffer = "";
		try {
			for await (const chunk of res.body as unknown as AsyncIterable<Uint8Array>) {
				bytes += chunk.byteLength;
				buffer += decoder.decode(chunk, { stream: true });
				let cut: number;
				while ((cut = buffer.indexOf("\n\n")) >= 0) {
					const block = buffer.slice(0, cut);
					buffer = buffer.slice(cut + 2);
					if (block.startsWith(":")) continue;
					const field = (name: string) => block.split("\n").find((l) => l.startsWith(`${name}: `))?.slice(name.length + 2);
					events.push({ id: field("id"), event: field("event")!, data: field("data")! });
				}
			}
		} catch {
			// aborted
		}
	})();
	return { events, done, bytes: () => bytes };
}

describe("the runtime's thread routes", () => {
	it("stream deltas that rebuild the view, steer a running reply, and page the transcript", async () => {
		const rt = await runtime();
		expect((await send(rt.base, { text: "hi", whenBusy: "sideways" })).status).toBe(400);
		expect((await send(rt.base, { text: "What is the answer?" })).status).toBe(202);
		const aborter = new AbortController();
		cleanup.push(async () => aborter.abort());
		const stream = follow(`${rt.base}/threads/t1/events?format=delta&v=1`, aborter.signal);
		const legacy = follow(`${rt.base}/threads/t1/events`, aborter.signal);
		await rt.started;
		while (stream.events.length === 0 || legacy.events.length === 0) await new Promise((r) => setTimeout(r, 20));
		// While the tool runs, a steer joins this reply; a follow-up waits for it to end.
		expect((await send(rt.base, { text: "Also mention the weather", whenBusy: "steer" })).status).toBe(202);
		expect((await send(rt.base, { text: "Thanks!", whenBusy: "followUp" })).status).toBe(202);
		rt.release();
		const final = await idle(rt.base, 7);
		await new Promise((r) => setTimeout(r, 300));
		aborter.abort();
		await Promise.all([stream.done, legacy.done]);

		// The steering message is applied to the running reply: placed after its tool round, before its answer, which
		// saw it; the follow-up comes after that answer and gets its own.
		const shape = final.messages.map((m) => (m.role === "tool" ? "tool" : `${m.role}:${m.text.slice(0, 80)}`));
		expect(shape).toEqual([
			"user:What is the answer?",
			"assistant:Let me look that up. ",
			"tool",
			"user:Also mention the weather",
			expect.stringMatching(/^assistant:Answer to: What is the answer\? \| Also mention the w/),
			"user:Thanks!",
			expect.stringMatching(/^assistant:Answer to: .*\| Thanks!/),
		]);

		// Applying the deltas in order gives the final view.
		expect(stream.events[0]!.event).toBe("snapshot");
		let client: ChatView | undefined;
		let seq = 0;
		const types = new Set<string>();
		for (const e of stream.events) {
			const event = JSON.parse(e.data) as DeltaEvent;
			expect(event.type).toBe(e.event);
			expect(event.seq).toBe(++seq);
			expect(e.id).toBe(String(seq));
			types.add(event.type);
			client = applyDelta(client, event);
		}
		expect(client).toEqual(final);
		for (const t of ["message.added", "message.updated", "partial.appended", "partial.cleared", "tool.updated", "tool.removed", "run"]) expect(types).toContain(t);
		// The running tool's events carry its elapsed time (it ran while the test held it).
		const toolUpdate = stream.events.map((e) => JSON.parse(e.data) as DeltaEvent).find((e) => e.type === "tool.updated" && e.outputAppended === " up");
		expect(toolUpdate).toMatchObject({ startedAt: expect.any(Number), elapsedMs: expect.any(Number) });
		// The whole-view stream is unchanged: every event a whole view, the last one the final view.
		expect(new Set(legacy.events.map((e) => e.event))).toEqual(new Set(["view"]));
		expect(JSON.parse(legacy.events.at(-1)!.data)).toEqual(final);
		expect(stream.bytes()).toBeLessThan(legacy.bytes());

		// A reconnect (Last-Event-ID) gets a snapshot first, numbered after the last event.
		const again = new AbortController();
		const resumed = follow(`${rt.base}/threads/t1/events?format=delta&once=true`, again.signal, { lastEventId: String(seq) });
		await resumed.done;
		expect(resumed.events).toHaveLength(1);
		expect(JSON.parse(resumed.events[0]!.data)).toEqual({ type: "snapshot", seq: seq + 1, v: 1, view: final });
		// A windowed snapshot: the latest messages and a cursor for the earlier ones.
		const windowed = follow(`${rt.base}/threads/t1/events?format=delta&once=true&limit=2`, again.signal);
		await windowed.done;
		const snap = JSON.parse(windowed.events[0]!.data) as Extract<DeltaEvent, { type: "snapshot" }>;
		expect(snap.view.messages).toEqual(final.messages.slice(-2));
		expect(snap.view.earlierCursor).toBeTypeOf("string");
		for (const bad of ["format=xml", "format=delta&v=2", "v=1", "limit=0"]) {
			expect((await fetch(`${rt.base}/threads/t1/events?${bad}&once=true`, { headers })).status, bad).toBe(400);
		}

		// Paging the transcript backwards gives every message once, in order; unpaged reads are unchanged.
		expect(await getView(rt.base)).toEqual(final);
		const seen: ChatMessage[] = [];
		let page = await getView(rt.base, "?limit=2");
		for (;;) {
			seen.unshift(...page.messages);
			if (page.earlierCursor === undefined) break;
			page = await getView(rt.base, `?limit=2&before=${page.earlierCursor}`);
		}
		expect(seen).toEqual(final.messages);
		expect((await fetch(`${rt.base}/threads/t1?before=nope`, { headers })).status).toBe(400);
	}, 30_000);
});
