import { timingSafeEqual } from "node:crypto";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type Harness, LiveDoc } from "@earendil-works/pi-durable";
import type { ThreadStore } from "./threads.ts";
import { buildView, type ChatView } from "./view.ts";

const ctx = BACKGROUND_CONTEXT;
const MAX_BODY = 256 * 1024;
const THREAD_ID = /^[A-Za-z0-9_-]{1,64}$/;

export type RuntimeOptions = {
	harness: Harness;
	threads: ThreadStore;
	/** Shared secret the platform presents; every route but /healthz requires it. */
	token: string;
	info: { agentId: string; name: string; model: string };
};

/**
 * The agent's private HTTP interface. Only the platform calls it, with the agent's bearer token; clients never reach a
 * runtime directly. Pi is the source of truth: the runtime keeps no state of its own besides the open harness.
 */
export function createRuntimeServer(options: RuntimeOptions): { server: Server; lastActivity: () => number } {
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
		if (!authorized(request)) return json(response, 401, { error: "unauthorized" });

		if (method === "GET" && url.pathname === "/status") {
			const busy = await busyThreads();
			if (busy > 0) touch();
			return json(response, 200, { ...options.info, busyThreads: busy, lastActivityAt: lastActivity });
		}
		if (method === "GET" && url.pathname === "/threads") return json(response, 200, { threads: await threads.list() });

		if (parts[0] !== "threads" || parts[1] === undefined) return json(response, 404, { error: "not found" });
		const threadId = parts[1];
		if (!THREAD_ID.test(threadId)) return json(response, 400, { error: "invalid thread id" });
		const action = parts[2];

		if (method === "GET" && action === undefined) {
			const view = await viewOf(threadId);
			return view === undefined ? json(response, 404, { error: "no such thread" }) : json(response, 200, view);
		}
		if (method === "DELETE" && action === undefined) {
			const conversation = await threads.get(threadId);
			if (conversation !== undefined) await conversation.abort(ctx).catch(() => {});
			return json(response, (await threads.remove(threadId)) ? 204 : 404, {});
		}
		if (method === "POST" && action === "messages") {
			touch();
			const body = (await readJson(request)) as { text?: unknown; requestId?: unknown };
			const text = typeof body.text === "string" ? body.text.trim() : "";
			const requestId = typeof body.requestId === "string" ? body.requestId : undefined;
			if (text.length === 0 || text.length > 32_000) return json(response, 400, { error: "text is required (at most 32,000 characters)" });
			if (requestId !== undefined && !THREAD_ID.test(requestId)) return json(response, 400, { error: "invalid requestId" });
			const conversation = await threads.ensure(threadId, text);
			const submission = await conversation.submit(
				{ type: "input", content: text, whenBusy: "followUp", ...(requestId === undefined ? {} : { requestId }) },
				ctx,
			);
			await threads.touch(threadId);
			return json(response, 202, { submissionId: submission.id });
		}
		if (method === "POST" && action === "stop") {
			touch();
			const conversation = await threads.get(threadId);
			if (conversation === undefined) return json(response, 404, { error: "no such thread" });
			await conversation.abort(ctx);
			return json(response, 202, {});
		}
		if (method === "GET" && action === "events") return streamThread(threadId, request, response);
		return json(response, 404, { error: "not found" });
	};

	/** Server-sent events: the whole chat view after every commit that touches the thread, coalesced to 10/s. */
	const streamThread = async (threadId: string, request: IncomingMessage, response: ServerResponse) => {
		const conversation = await threads.get(threadId);
		if (conversation === undefined) return json(response, 404, { error: "no such thread" });
		const watch = await conversation.watch(ctx);
		response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store", connection: "keep-alive" });
		const send = (value: typeof watch.value) => {
			response.write(`event: view\ndata: ${JSON.stringify(buildView(value.entries, value.docs["pi.live"] as never))}\n\n`);
		};
		send(watch.value);
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

	const server = createServer((request, response) => {
		route(request, response).catch((error: unknown) => {
			const status = error instanceof RequestError ? error.status : 500;
			if (status === 500) console.error("[runtime] request failed", error);
			if (!response.headersSent) json(response, status, { error: status === 500 ? "internal error" : (error as Error).message });
			else response.end();
		});
	});
	return { server, lastActivity: () => lastActivity };
}

class RequestError extends Error {
	constructor(
		readonly status: number,
		message: string,
	) {
		super(message);
	}
}

function json(response: ServerResponse, status: number, body: unknown): void {
	if (status === 204) {
		response.writeHead(204).end();
		return;
	}
	response.writeHead(status, { "content-type": "application/json", "cache-control": "no-store" }).end(JSON.stringify(body));
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
