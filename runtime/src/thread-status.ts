import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type Harness, LiveDoc } from "@earendil-works/pi-durable";
import type { ThreadStore } from "./threads.ts";
import { buildView } from "./view.ts";

const ctx = BACKGROUND_CONTEXT;

/** A thread's status, as the platform records it (agent_threads). */
export type ThreadStatus = {
	threadId: string;
	title: string;
	createdAt: number;
	busy: boolean;
	lastReplyAt?: number;
	lastError?: string;
	/** When this status was observed: the platform keeps the newest. */
	at: number;
};

type Post = (path: string, body: unknown) => Promise<void>;

/**
 * Tells the platform about this agent's threads, so it can list them without waking the agent: every thread when the
 * runtime starts, and each reply's end (with its error, if it failed). Replies are watched by polling the thread's
 * live document while it runs. Reporting is best effort and retried; the next start reconciles anything missed.
 */
export class ThreadStatusReporter {
	/** Threads with a reply in progress, and the submission (message) each reply answers, when known. */
	private readonly watching = new Map<string, number | undefined>();
	private timer: ReturnType<typeof setInterval> | undefined;
	private queue: Promise<void> = Promise.resolve();
	/** Called when a watched reply ends. */
	onReplyEnd: (() => void) | undefined;

	constructor(
		private readonly harness: Harness,
		private readonly threads: ThreadStore,
		private readonly post: Post,
		private readonly pollMs = 500,
	) {}

	/** Reports every thread (the runtime just started) and watches those with a reply in progress. */
	async sync(): Promise<void> {
		const at = Date.now();
		const statuses: ThreadStatus[] = [];
		for (const t of await this.threads.list()) {
			const busy = await this.running(t.conversationId);
			statuses.push({ threadId: t.threadId, title: t.title, createdAt: t.createdAt, busy, at });
			if (busy) this.watch(t.threadId);
		}
		await this.send("/threads/sync", { threads: statuses, at });
	}

	/** A message was accepted: report the reply's end (and, if it went unanswered, why). */
	watch(threadId: string, submissionId?: number): void {
		this.watching.set(threadId, submissionId);
		this.timer ??= setInterval(() => void this.poll(), this.pollMs);
	}

	private async running(conversationId: number): Promise<boolean> {
		const live = await this.harness.snapshot(LiveDoc, conversationId as never, ctx);
		return live?.run !== undefined;
	}

	private polling = false;
	private async poll(): Promise<void> {
		if (this.polling) return;
		this.polling = true;
		try {
			for (const [threadId, submissionId] of [...this.watching]) {
				const records = await this.threads.list();
				const record = records.find((r) => r.threadId === threadId);
				if (record === undefined) {
					this.watching.delete(threadId);
					continue;
				}
				if (await this.running(record.conversationId)) continue;
				this.watching.delete(threadId);
				this.onReplyEnd?.();
				const now = Date.now();
				void this.send("/threads/report", {
					threadId,
					title: record.title,
					createdAt: record.createdAt,
					busy: false,
					lastReplyAt: now,
					lastError: await this.lastError(threadId, submissionId),
					at: now,
				} satisfies ThreadStatus);
			}
			if (this.watching.size === 0 && this.timer) {
				clearInterval(this.timer);
				this.timer = undefined;
			}
		} catch (error) {
			console.error("[runtime] thread status:", (error as Error).message);
		} finally {
			this.polling = false;
		}
	}

	/**
	 * The last reply's error, if it failed: the error of the last assistant message, or else why the message went
	 * unanswered. pi ends some runs without writing a reply at all (no model, a fault, a reset); then only the
	 * submission records why.
	 */
	private async lastError(threadId: string, submissionId?: number): Promise<string | undefined> {
		const fromReply = await this.replyError(threadId);
		if (fromReply !== undefined || submissionId === undefined) return fromReply;
		try {
			const submission = await this.harness.submission(submissionId as never, ctx);
			const record = submission === undefined ? undefined : await submission.status(ctx);
			if (record?.status !== "unanswered") return undefined;
			const detail = record.detail;
			return unansweredError(record.reason, typeof detail === "string" || detail == null ? (detail ?? undefined) : JSON.stringify(detail));
		} catch (error) {
			console.error("[runtime] thread status:", (error as Error).message);
			return undefined;
		}
	}

	private async replyError(threadId: string): Promise<string | undefined> {
		const conversation = await this.threads.get(threadId);
		if (conversation === undefined) return undefined;
		const watch = await conversation.watch(ctx);
		const value = watch.value;
		await watch.stop();
		const view = buildView(value.entries, value.docs["pi.live"] as never);
		const last = view.messages.at(-1);
		return last?.role === "assistant" && last.error ? last.error.slice(0, 2000) : undefined;
	}

	/** Sends in order; a failed send is retried a few times before giving up (the next start reconciles). */
	private send(path: string, body: unknown): Promise<void> {
		this.queue = this.queue.then(async () => {
			for (let attempt = 0; attempt < 5; attempt++) {
				try {
					await this.post(path, body);
					return;
				} catch (error) {
					if (attempt === 4) console.error(`[runtime] reporting ${path} failed:`, (error as Error).message);
					await new Promise((r) => setTimeout(r, 1000 * 2 ** attempt));
				}
			}
		});
		return this.queue;
	}

	stop(): Promise<void> {
		if (this.timer) clearInterval(this.timer);
		this.timer = undefined;
		return this.queue;
	}
}

/** Why a message went unanswered, for people (undefined: stopped on purpose, which isn't an error). */
export function unansweredError(reason: string | undefined, detail: string | undefined): string | undefined {
	const more = detail ? `: ${detail.slice(0, 1800)}` : "";
	switch (reason) {
		case "aborted":
			return undefined;
		case "no_model":
			return "No reply: the agent's model isn't available to this conversation" + more;
		case "model_error":
			return "No reply: the model request failed" + more;
		case "faulted":
			return "No reply: the agent failed" + more;
		case "reset":
			return "No reply: the conversation was reset before the agent answered";
		case "stale":
			return "No reply: the message expired before the agent could answer";
		default:
			return `No reply (${reason ?? "unknown reason"})` + more;
	}
}

/** Posts JSON to the platform's gateway with the agent's credential. */
export function gatewayPost(baseUrl: string, key: string, fetchImpl: typeof fetch = fetch): Post {
	return async (path, body) => {
		const res = await fetchImpl(baseUrl.replace(/\/$/, "") + path, {
			method: "POST",
			headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
			body: JSON.stringify(body),
			signal: AbortSignal.timeout(10_000),
		});
		if (!res.ok) throw new Error(`status ${res.status}`);
	};
}
