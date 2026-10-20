import { timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type RequestListener, type Server, type ServerResponse } from "node:http";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type Harness, LiveDoc, type Conversation } from "@earendil-works/pi-durable";
import type { ThreadStore } from "./threads.ts";
import { DELTA_VERSION, DeltaStream } from "./delta.ts";
import { CONTRACT_VERSION, PI_DURABLE_VERSION } from "./version.ts";
import { AUTHOR_KINDS, type Author, buildMessages, buildView, type ChatMessage, type ChatView, liveParts, type MessagePage, pageView, parsePage, USER_ENTRY, type UserEntryData } from "./view.ts";

const ctx = BACKGROUND_CONTEXT;
const MAX_BODY = 256 * 1024;
const THREAD_ID = /^[A-Za-z0-9_-]{1,64}$/;

export type RuntimeOptions = {
	harness: Harness;
	threads: ThreadStore;
	/** Shared secret the platform presents; every route but /healthz requires it. */
	token: string;
	info: { agentId: string; name: string; model: string };
	/** Prepares a thread before a message (its MCP servers), after it exists. */
	beforeMessage?: (threadId: string, conversation: Conversation) => Promise<void>;
	/** A message was submitted to the thread (its reply's end is reported to the platform). */
	afterMessage?: (threadId: string, submissionId: number) => void;
};

/**
 * The agent's private HTTP interface. Only the platform calls it, with the agent's bearer token; clients never reach a
 * runtime directly. Pi is the source of truth: the runtime keeps no state of its own besides the open harness.
 */
export function createRuntimeServer(options: RuntimeOptions): {
	server: Server;
	handler: RequestListener;
	lastActivity: () => number;
	/** Records activity now (a reply just ended, for instance), so the reaper doesn't hibernate an agent that just worked. */
	touch: () => void;
} {
	const { harness, threads } = options;
	const expected = Buffer.from(`Bearer ${options.token}`);
	let lastActivity = Date.now();
	const touch = () => {
		lastActivity = Date.now();
	};
	// Open event streams. The platform's replicas don't share what each one has seen, so the runtime, which serves every
	// stream, says how many are open (/status): whichever replica reaps idle agents asks here, never its own records.
	let openStreams = 0;

	const authorized = (request: IncomingMessage) => {
		const header = Buffer.from(request.headers.authorization ?? "");
		return header.length === expected.length && timingSafeEqual(header, expected);
	};

	const busyThreads = async () => {
		let busy = 0;
		for (const thread of await threads.list()) {
			const live = await harness.snapshot(LiveDoc, thread.conversationId as never, ctx);
			if (live?.run !== undefined) busy++;
		}
		return busy;
	};

	const viewOf = async (threadId: string): Promise<ChatView | undefined> => {
		const conversation = await threads.get(threadId);
		if (conversation === undefined) return undefined;
		const watch = await conversation.watch(ctx);
		const value = watch.value;
		await watch.stop();
		return buildView(value.entries, value.docs["pi.live"] as never);
	};

	const route = async (request: IncomingMessage, response: ServerResponse) => {
		const url = new URL(request.url ?? "/", "http://runtime");
		const parts = url.pathname.split("/").filter(Boolean);
		const method = request.method ?? "GET";

		if (method === "GET" && url.pathname === "/healthz") return json(response, 200, { ok: true });
		if (!authorized(request)) return fail(response, 401, "unauthorized", "Unauthorized");

		if (method === "GET" && url.pathname === "/status") {
			const busy = await busyThreads();
			if (busy > 0) touch();
			return json(response, 200, {
				...options.info,
				busyThreads: busy,
				openStreams,
				lastActivityAt: lastActivity,
				contractVersion: CONTRACT_VERSION,
				piDurableVersion: PI_DURABLE_VERSION,
			});
		}
		if (method === "GET" && url.pathname === "/threads") return json(response, 200, await threads.list());

		if (parts[0] !== "threads" || parts[1] === undefined) return fail(response, 404, "not_found", "Not found");
		const threadId = parts[1];
		if (!THREAD_ID.test(threadId)) return fail(response, 400, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores");
		const action = parts[2];

		if (method === "GET" && action === undefined) {
			const { page, error } = parsePage(url.searchParams);
			if (error !== undefined) return fail(response, 400, error.code, error.message);
			// Someone reading a thread (often right after its reply) is using the agent.
			touch();
			const view = await viewOf(threadId);
			if (view === undefined) return fail(response, 404, "thread_not_found", "Thread not found");
			return json(response, 200, page === undefined ? view : pageView(view, page));
		}
		if (method === "DELETE" && action === undefined) {
			const conversation = await threads.get(threadId);
			if (conversation !== undefined) await conversation.abort(ctx).catch(() => {});
			return (await threads.remove(threadId)) ? json(response, 200, { ok: true }) : fail(response, 404, "thread_not_found", "Thread not found");
		}
		if (method === "POST" && action === "messages") {
			touch();
			const body = (await readJson(request)) as { text?: unknown; requestId?: unknown; author?: unknown; announceAuthor?: unknown; whenBusy?: unknown };
			const text = typeof body.text === "string" ? body.text.trim() : "";
			const requestId = typeof body.requestId === "string" ? body.requestId : undefined;
			if (text.length === 0 || text.length > 32_000) return fail(response, 400, "invalid_text", "text is required (at most 32,000 characters)");
			if (requestId !== undefined && !THREAD_ID.test(requestId)) return fail(response, 400, "invalid_request_id", "requestId must be 1-64 letters, digits, dashes or underscores");
			// While a reply runs: a follow-up waits for it to end; a steer joins it after its current tool round.
			const whenBusy = body.whenBusy ?? "followUp";
			if (whenBusy !== "followUp" && whenBusy !== "steer") return fail(response, 400, "invalid_when_busy", "whenBusy must be followUp or steer");
			// The platform (the only caller) says who sent the message, and whether the model is told (shared threads).
			const author = parseAuthor(body.author);
			const conversation = await threads.ensure(threadId, text);
			await options.beforeMessage?.(threadId, conversation);
			const data: UserEntryData = { text, at: Date.now(), ...(author === undefined ? {} : { author }) };
			const content = author !== undefined && body.announceAuthor === true ? `${speakerLine(author)}\n${text}` : text;
			const submission = await conversation.submit(
				{
					type: "input",
					content,
					whenBusy,
					entry: { kind: USER_ENTRY, data },
					...(requestId === undefined ? {} : { requestId }),
				},
				ctx,
			);
			await threads.touch(threadId);
			options.afterMessage?.(threadId, submission.id as number);
			return json(response, 202, { submissionId: submission.id });
		}
		if (method === "POST" && action === "stop") {
			touch();
			const conversation = await threads.get(threadId);
			if (conversation === undefined) return fail(response, 404, "thread_not_found", "Thread not found");
			await conversation.abort(ctx);
			return json(response, 202, { ok: true });
		}
		if (method === "GET" && action === "events") {
			const options = streamOptions(url.searchParams, request.headers["last-event-id"]);
			if ("error" in options) return fail(response, 400, options.error.code, options.error.message);
			return streamThread(threadId, request, response, options);
		}
		return fail(response, 404, "not_found", "Not found");
	};

	/**
	 * Server-sent events, coalesced to 10/s: by default the whole chat view after every commit that touches the thread
	 * (`view` events); with `format=delta` a snapshot and then only the changes (see delta.ts). `limit` keeps either to
	 * the latest messages.
	 */
	const streamThread = async (threadId: string, request: IncomingMessage, response: ServerResponse, options: StreamOptions) => {
		const conversation = await threads.get(threadId);
		if (conversation === undefined) return fail(response, 404, "thread_not_found", "Thread not found");
		const watch = await conversation.watch(ctx);
		response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store", connection: "keep-alive" });
		touch();
		// The transcript is rebuilt only when the entries changed: a frame that only moves the reply in progress or a
		// tool keeps the same entries (pi-durable shares unchanged parts between frames).
		let entries: unknown;
		let messages: ChatMessage[] = [];
		const current = (value: typeof watch.value): { view: ChatView; changed: boolean } => {
			const changed = value.entries !== entries;
			if (changed) {
				entries = value.entries;
				messages = buildMessages(value.entries);
			}
			const live = value.docs["pi.live"] as never as Parameters<typeof liveParts>[0];
			return { view: { busy: live?.run !== undefined, messages, ...liveParts(live) }, changed };
		};
		const deltas = options.format === "delta" ? new DeltaStream(options.limit, options.lastSeq) : undefined;
		const page: MessagePage | undefined = options.limit === undefined ? undefined : { limit: options.limit };
		const send = (value: typeof watch.value) => {
			const { view, changed } = current(value);
			if (deltas === undefined) {
				response.write(`event: view\ndata: ${JSON.stringify(page === undefined ? view : pageView(view, page))}\n\n`);
				return;
			}
			let out = "";
			for (const event of deltas.next(view, changed)) out += `id: ${event.seq}\nevent: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`;
			if (out !== "") response.write(out);
		};
		send(watch.value);
		if (options.once) {
			await watch.stop();
			response.end();
			return;
		}
		let pending: typeof watch.value | undefined;
		let timer: NodeJS.Timeout | undefined;
		watch.start(async (value) => {
			touch();
			pending = value;
			timer ??= setTimeout(() => {
				timer = undefined;
				if (pending !== undefined) send(pending);
				pending = undefined;
			}, 100);
		});
		const keepAlive = setInterval(() => response.write(": keep-alive\n\n"), 15_000);
		openStreams++;
		let ended = false;
		const end = () => {
			clearInterval(keepAlive);
			if (timer !== undefined) clearTimeout(timer);
			void watch.stop();
			if (ended) return;
			ended = true;
			openStreams--;
			// Someone was watching until now: the idle clock starts here.
			touch();
		};
		request.on("close", end);
		void watch.closed.then(() => {
			end();
			response.end();
		});
	};

	const handler: RequestListener = (request, response) => {
		route(request, response).catch((error: unknown) => {
			const status = error instanceof RequestError ? error.status : 500;
			if (status === 500) console.error("[runtime] request failed", error);
			if (!response.headersSent) fail(response, status, status === 500 ? "internal" : "invalid_request", status === 500 ? "Internal error" : (error as Error).message);
			else response.end();
		});
	};
	return { server: createServer(handler), handler, lastActivity: () => lastActivity, touch };
}

type StreamOptions = { once: boolean; format: "view" | "delta"; limit?: number; lastSeq: number };

/**
 * An event stream's options: `once`, `format` (view, the default, or delta), `v` (the delta format's version: 1),
 * `limit` (the latest messages only) and, for deltas, the Last-Event-ID a reconnecting client sends (its sequence
 * goes on from there, after a new snapshot).
 */
export function streamOptions(query: URLSearchParams, lastEventId: string | string[] | undefined): StreamOptions | { error: { code: string; message: string } } {
	const format = query.get("format") ?? "view";
	if (format !== "view" && format !== "delta") return { error: { code: "invalid_format", message: "format must be view or delta" } };
	const v = query.get("v");
	if (v !== null && (format !== "delta" || v !== String(DELTA_VERSION))) {
		return { error: { code: "unsupported_version", message: `The delta format's version is ${DELTA_VERSION}` } };
	}
	let limit: number | undefined;
	if (query.get("limit") !== null) {
		const { page, error } = parsePage(new URLSearchParams({ limit: query.get("limit")! }));
		if (error !== undefined) return { error };
		limit = page!.limit;
	}
	const last = typeof lastEventId === "string" && /^\d{1,15}$/.test(lastEventId) ? Number(lastEventId) : 0;
	return { once: query.get("once") === "true", format, ...(limit === undefined ? {} : { limit }), lastSeq: format === "delta" ? last : 0 };
}

/** The author the platform sent, or undefined when absent or malformed. */
function parseAuthor(value: unknown): Author | undefined {
	if (typeof value !== "object" || value === null) return undefined;
	const { kind, id, name, via } = value as Record<string, unknown>;
	if (!AUTHOR_KINDS.has(kind as string) || typeof id !== "string" || typeof name !== "string") return undefined;
	const clean = (s: string) => s.replace(/[\u0000-\u001f\u007f]+/g, " ").trim().slice(0, 200);
	return {
		kind: kind as Author["kind"],
		id: id.slice(0, 64),
		name: clean(name),
		...(typeof via === "string" && clean(via) !== "" ? { via: clean(via) } : {}),
	};
}

/** The line that tells the model who is speaking (shared threads, where several people talk to the agent). */
export function speakerLine(author: Author): string {
	const name = author.name === "" ? "someone" : author.name;
	// A trigger's run (a schedule or a webhook): no person is speaking.
	if (author.kind === "trigger") return `[Message from trigger ${name}]`;
	return `[Message from ${name}${author.via === undefined ? "" : ` via ${author.via}`}]`;
}

class RequestError extends Error {
	constructor(
		readonly status: number,
		message: string,
	) {
		super(message);
	}
}

/** Platform conventions: `{"data": …}` on success, `{"error": {"code", "message"}}` otherwise. */
function json(response: ServerResponse, status: number, body: unknown): void {
	if (status === 204) {
		response.writeHead(204).end();
		return;
	}
	response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify({ data: body }));
}

function fail(response: ServerResponse, status: number, code: string, message: string): void {
	response
		.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" })
		.end(JSON.stringify({ error: { code, message } }));
}

async function readJson(request: IncomingMessage): Promise<unknown> {
	const chunks: Buffer[] = [];
	let size = 0;
	for await (const chunk of request) {
		size += (chunk as Buffer).length;
		if (size > MAX_BODY) throw new RequestError(413, "request body too large");
		chunks.push(chunk as Buffer);
	}
	try {
		return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
	} catch {
		throw new RequestError(400, "invalid JSON");
	}
}
