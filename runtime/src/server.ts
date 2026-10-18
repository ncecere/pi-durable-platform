import { timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type RequestListener, type Server, type ServerResponse } from "node:http";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type Harness, LiveDoc, type Conversation } from "@earendil-works/pi-durable";
import type { ThreadStore } from "./threads.ts";
import { type Author, buildView, type ChatView, USER_ENTRY, type UserEntryData } from "./view.ts";

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
			return json(response, 200, { ...options.info, busyThreads: busy, lastActivityAt: lastActivity });
		}
		if (method === "GET" && url.pathname === "/threads") return json(response, 200, await threads.list());

		if (parts[0] !== "threads" || parts[1] === undefined) return fail(response, 404, "not_found", "Not found");
		const threadId = parts[1];
		if (!THREAD_ID.test(threadId)) return fail(response, 400, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores");
		const action = parts[2];

		if (method === "GET" && action === undefined) {
			// Someone reading a thread (often right after its reply) is using the agent.
			touch();
			const view = await viewOf(threadId);
			return view === undefined ? fail(response, 404, "thread_not_found", "Thread not found") : json(response, 200, view);
		}
		if (method === "DELETE" && action === undefined) {
			const conversation = await threads.get(threadId);
			if (conversation !== undefined) await conversation.abort(ctx).catch(() => {});
			return (await threads.remove(threadId)) ? json(response, 200, { ok: true }) : fail(response, 404, "thread_not_found", "Thread not found");
		}
		if (method === "POST" && action === "messages") {
			touch();
			const body = (await readJson(request)) as { text?: unknown; requestId?: unknown; author?: unknown; announceAuthor?: unknown };
			const text = typeof body.text === "string" ? body.text.trim() : "";
			const requestId = typeof body.requestId === "string" ? body.requestId : undefined;
			if (text.length === 0 || text.length > 32_000) return fail(response, 400, "invalid_text", "text is required (at most 32,000 characters)");
			if (requestId !== undefined && !THREAD_ID.test(requestId)) return fail(response, 400, "invalid_request_id", "requestId must be 1-64 letters, digits, dashes or underscores");
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
					whenBusy: "followUp",
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
		if (method === "GET" && action === "events") return streamThread(threadId, request, response, url.searchParams.get("once") === "true");
		return fail(response, 404, "not_found", "Not found");
	};

	/** Server-sent events: the whole chat view after every commit that touches the thread, coalesced to 10/s. */
	const streamThread = async (threadId: string, request: IncomingMessage, response: ServerResponse, once: boolean) => {
		const conversation = await threads.get(threadId);
		if (conversation === undefined) return fail(response, 404, "thread_not_found", "Thread not found");
		const watch = await conversation.watch(ctx);
		response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store", connection: "keep-alive" });
		const send = (value: typeof watch.value) => {
			response.write(`event: view\ndata: ${JSON.stringify(buildView(value.entries, value.docs["pi.live"] as never))}\n\n`);
		};
		send(watch.value);
		if (once) {
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
		const end = () => {
			clearInterval(keepAlive);
			if (timer !== undefined) clearTimeout(timer);
			void watch.stop();
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

/** The author the platform sent, or undefined when absent or malformed. */
function parseAuthor(value: unknown): Author | undefined {
	if (typeof value !== "object" || value === null) return undefined;
	const { kind, id, name, via } = value as Record<string, unknown>;
	if ((kind !== "user" && kind !== "service_account") || typeof id !== "string" || typeof name !== "string") return undefined;
	const clean = (s: string) => s.replace(/[\u0000-\u001f\u007f]+/g, " ").trim().slice(0, 200);
	return {
		kind,
		id: id.slice(0, 64),
		name: clean(name),
		...(typeof via === "string" && clean(via) !== "" ? { via: clean(via) } : {}),
	};
}

/** The line that tells the model who is speaking (shared threads, where several people talk to the agent). */
export function speakerLine(author: Author): string {
	const name = author.name === "" ? "someone" : author.name;
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
