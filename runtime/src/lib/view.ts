import type { AssistantMessage, Message } from "@earendil-works/pi-ai";
import type { EntryRecord, LiveState } from "@earendil-works/pi-durable";

/**
 * Who sent a user message, as the platform says (never as its client says): a person, a service account, or a trigger
 * (an unattended run the platform started). `via`: the app a person used.
 */
export type Author = { kind: "user" | "service_account" | "trigger"; id: string; name: string; via?: string };

/** The author kinds the platform sends. */
export const AUTHOR_KINDS: ReadonlySet<string> = new Set(["user", "service_account", "trigger"]);

/**
 * The entry a user message is placed as: pi's user message (what the model sees, which may start with a line naming
 * the speaker) plus `data`, what the transcript shows: the text as sent, when, and by whom.
 */
export const USER_ENTRY = "pdp.user";
export type UserEntryData = { text: string; at: number; author?: Author };

/** The chat view the platform relays to clients: transcript plus the reply in progress. */
export type ChatView = {
	busy: boolean;
	messages: ChatMessage[];
	/** The assistant reply being generated, if any. */
	partial?: ChatPartial;
	/** Tool calls of the current round. */
	tools: ChatTool[];
	/**
	 * Paged reads only (`limit` or `before` given): pass it as `before` for the earlier messages. Absent when there are
	 * none, and on unpaged reads.
	 */
	earlierCursor?: string;
	/**
	 * The documents the agent exposes to apps (docs/custom-agents.md, Documents for apps) that exist in this thread, by
	 * kind, with each one's revision: it changes when the document does (read it with
	 * `GET /threads/{thread}/documents/{kind}`). Absent when the agent exposes none.
	 */
	documents?: Record<string, ChatDocumentRef>;
	/**
	 * Tool calls of this thread waiting for a person's approval (docs/approvals.md), with a redacted view of their
	 * arguments; calls in a subagent's conversation too. Absent when there are none.
	 */
	approvals?: ChatApproval[];
};

/** An exposed document in the view: its revision (a hash of its content; equal content, equal revision). */
export type ChatDocumentRef = { revision: string };

export type ChatPartial = { text: string; thinking: string };
/**
 * A tool call of the current round. `details`: what the tool published for UIs so far (its last `api.details()`),
 * JSON, at most MAX_DETAILS_BYTES; `detailsOmitted` says it was larger and left out.
 */
export type ChatTool = { callId: string; name: string; status: string; output?: string } & ChatDetails;

/** A tool's `details` (pi-durable's channel for UIs), bounded: see boundedDetails. */
export type ChatDetails = { details?: unknown; detailsOmitted?: true };

/** The most a tool's details may take in the view, as JSON (larger ones are left out, with `detailsOmitted`). */
export const MAX_DETAILS_BYTES = 16 * 1024;

/** A tool's details as the view carries them: JSON only, at most MAX_DETAILS_BYTES, else omitted (and said so). */
export function boundedDetails(value: unknown): ChatDetails {
	if (value === undefined || value === null) return {};
	let json: string | undefined;
	try {
		json = JSON.stringify(value);
	} catch {
		return { detailsOmitted: true };
	}
	if (json === undefined) return {};
	if (Buffer.byteLength(json, "utf8") > MAX_DETAILS_BYTES) return { detailsOmitted: true };
	return { details: value };
}

export type ChatToolCall = { id: string; name: string; arguments: unknown; startedAt?: number; endedAt?: number; approval?: ChatApproval };

/**
 * A request to approve a tool call: pending, or how it was decided. On a transcript's tool call (without `arguments`,
 * which the call has) and in the view's pending list.
 */
export type ChatApproval = {
	id: string;
	callId: string;
	tool: string;
	status: "pending" | "approved" | "denied" | "timed_out" | "cancelled";
	requestedAt: number;
	expiresAt?: number;
	summary?: string;
	requestedFor?: Author;
	decidedAt?: number;
	decidedBy?: { kind: "user" | "service_account" | "system"; id?: string; name: string };
	reason?: string;
	arguments?: unknown;
};

/** A thread's approvals as the view shows them: on the transcript's calls (by call ID), and the pending ones. */
export type ViewApprovals = { byCall: ReadonlyMap<string, ChatApproval>; pending: ChatApproval[] };
export type ChatUsage = { input: number; output: number; cacheRead: number; cacheWrite: number; total: number };

/** `at`: Unix milliseconds (a user message's sending, an assistant reply's start). */
export type ChatMessage =
	| { id: string; role: "user"; text: string; author?: Author; at?: number }
	| {
			id: string;
			role: "assistant";
			text: string;
			thinking?: string;
			toolCalls?: ChatToolCall[];
			error?: string;
			at?: number;
			model?: { provider: string; id: string };
			usage?: ChatUsage;
			stopReason?: string;
	  }
	| ({ id: string; role: "tool"; toolCallId: string; name: string; text: string; isError: boolean; at?: number } & ChatDetails);

export function buildView(
	entries: readonly EntryRecord[],
	live: Readonly<LiveState> | undefined,
	documents?: Record<string, ChatDocumentRef>,
	approvals?: ViewApprovals,
): ChatView {
	return {
		busy: live?.run !== undefined,
		messages: buildMessages(entries, approvals?.byCall),
		...liveParts(live),
		...(documents === undefined ? {} : { documents }),
		...(approvals === undefined || approvals.pending.length === 0 ? {} : { approvals: approvals.pending }),
	};
}

/**
 * The transcript part of the view: it changes only when entries (or the approvals on its calls) do; the delta stream
 * reuses it otherwise.
 */
export function buildMessages(entries: readonly EntryRecord[], approvals?: ReadonlyMap<string, ChatApproval>): ChatMessage[] {
	// When each tool call's result arrived, for the calls' endedAt.
	const resultAt = new Map<string, number>();
	for (const entry of entries) {
		for (const message of entry.model ?? []) {
			if (message.role === "toolResult" && typeof message.timestamp === "number") resultAt.set(message.toolCallId, message.timestamp);
		}
	}
	const messages: ChatMessage[] = [];
	for (const entry of entries) {
		if (entry.kind === "pi.system" || entry.kind === "pi.compaction") continue;
		const data = entry.kind === USER_ENTRY ? userData(entry.data) : undefined;
		(entry.model ?? []).forEach((message, index) => {
			const id = index === 0 ? `e${entry.id}` : `e${entry.id}:${index}`;
			const converted = convert(id, message as Message, resultAt, approvals);
			if (converted === undefined) return;
			if (converted.role === "user" && data !== undefined) {
				// The text as sent: the model's copy may start with a line naming the speaker.
				converted.text = data.text ?? converted.text;
				if (data.at !== undefined) converted.at = data.at;
				if (data.author !== undefined) converted.author = data.author;
			}
			messages.push(converted);
		});
	}
	return messages;
}

/** The live part of the view: the reply in progress and the current round's tools. */
export function liveParts(live: Readonly<LiveState> | undefined): Pick<ChatView, "partial" | "tools"> {
	const partialMessage = live?.generation?.message as AssistantMessage | undefined;
	return {
		...(partialMessage === undefined
			? {}
			: { partial: { text: textOf(partialMessage), thinking: thinkingOf(partialMessage) } }),
		tools: (live?.tools ?? []).map((slot) => ({
			callId: slot.callId,
			name: slot.name,
			status: slot.status,
			...(slot.output === undefined ? {} : { output: slot.output }),
			...boundedDetails((slot as { details?: unknown }).details),
		})),
	};
}

/** A pdp.user entry's data, read defensively (it is stored JSON). */
function userData(value: unknown): Partial<UserEntryData> | undefined {
	if (typeof value !== "object" || value === null) return undefined;
	const { text, at, author } = value as Record<string, unknown>;
	const out: Partial<UserEntryData> = {};
	if (typeof text === "string") out.text = text;
	if (typeof at === "number") out.at = at;
	if (typeof author === "object" && author !== null) {
		const { kind, id, name, via } = author as Record<string, unknown>;
		if (typeof kind === "string" && AUTHOR_KINDS.has(kind) && typeof id === "string" && typeof name === "string") {
			out.author = { kind: kind as Author["kind"], id, name, ...(typeof via === "string" ? { via } : {}) };
		}
	}
	return out;
}

const timeOf = (message: { timestamp?: unknown }) => (typeof message.timestamp === "number" && message.timestamp > 0 ? message.timestamp : undefined);

function convert(id: string, message: Message, resultAt: ReadonlyMap<string, number>, approvals?: ReadonlyMap<string, ChatApproval>): ChatMessage | undefined {
	const at = timeOf(message as { timestamp?: unknown });
	switch (message.role) {
		case "user":
			return {
				id,
				role: "user",
				text: typeof message.content === "string" ? message.content : message.content.map((p) => (p.type === "text" ? p.text : "[image]")).join("\n"),
				...(at === undefined ? {} : { at }),
			};
		case "assistant": {
			// Tool calls are made when the response ends (its start plus its duration, when known).
			const calledAt = at === undefined ? undefined : at + (typeof message.durationMs === "number" ? message.durationMs : 0);
			const toolCalls = message.content.flatMap((block): ChatToolCall[] => {
				if (block.type !== "toolCall") return [];
				const endedAt = resultAt.get(block.id);
				const approval = approvals?.get(block.id);
				return [
					{
						id: block.id,
						name: block.name,
						arguments: block.arguments ?? {},
						...(calledAt === undefined ? {} : { startedAt: calledAt }),
						...(endedAt === undefined ? {} : { endedAt }),
						...(approval === undefined ? {} : { approval }),
					},
				];
			});
			const thinking = thinkingOf(message);
			const error =
				message.stopReason === "aborted" ? "Interrupted" : message.stopReason === "error" ? (message.errorMessage ?? "The model request failed") : undefined;
			const usage = message.usage;
			return {
				id,
				role: "assistant",
				text: textOf(message),
				...(thinking ? { thinking } : {}),
				...(toolCalls.length > 0 ? { toolCalls } : {}),
				...(error === undefined ? {} : { error }),
				...(at === undefined ? {} : { at }),
				...(typeof message.provider === "string" && typeof message.model === "string" ? { model: { provider: message.provider, id: message.model } } : {}),
				...(usage === undefined
					? {}
					: {
							usage: {
								input: usage.input ?? 0,
								output: usage.output ?? 0,
								cacheRead: usage.cacheRead ?? 0,
								cacheWrite: usage.cacheWrite ?? 0,
								total: usage.totalTokens ?? 0,
							},
						}),
				...(typeof message.stopReason === "string" ? { stopReason: message.stopReason } : {}),
			};
		}
		case "toolResult":
			return {
				id,
				role: "tool",
				toolCallId: message.toolCallId,
				name: message.toolName,
				text: message.content.map((p) => (p.type === "text" ? p.text : "[image]")).join("\n"),
				isError: message.isError,
				...(at === undefined ? {} : { at }),
				...boundedDetails((message as { details?: unknown }).details),
			};
		default:
			return undefined;
	}
}

const textOf = (message: AssistantMessage) => message.content.flatMap((b) => (b.type === "text" ? [b.text] : [])).join("\n\n");
const thinkingOf = (message: AssistantMessage) =>
	message.content.flatMap((b) => (b.type === "thinking" && !b.redacted ? [b.thinking] : [])).join("\n\n");

/** A page of the transcript: the `limit` messages before the message `before` names (the newest when absent). */
export type MessagePage = { before?: string; limit: number };

export const DEFAULT_PAGE = 50;
export const MAX_PAGE = 200;
const MESSAGE_ID = /^e\d{1,15}(:\d{1,6})?$/;

/** The opaque cursor naming a message (clients pass it back as `before`). */
export function messageCursor(id: string): string {
	return Buffer.from(`m:${id}`, "utf8").toString("base64url");
}

/** The message a cursor names, or undefined when it is malformed. */
export function cursorMessage(cursor: string): string | undefined {
	if (cursor.length > 64 || !/^[A-Za-z0-9_-]+$/.test(cursor)) return undefined;
	const raw = Buffer.from(cursor, "base64url").toString("utf8");
	if (!raw.startsWith("m:")) return undefined;
	const id = raw.slice(2);
	return MESSAGE_ID.test(id) && messageCursor(id) === cursor ? id : undefined;
}

/**
 * Reads `?before=` and `?limit=`: undefined when neither is given (the whole transcript, as before paging existed),
 * a page otherwise, or an error for a malformed value.
 */
export function parsePage(query: URLSearchParams): { page?: MessagePage; error?: { code: string; message: string } } {
	const rawLimit = query.get("limit");
	const rawBefore = query.get("before");
	if (rawLimit === null && rawBefore === null) return {};
	let limit = DEFAULT_PAGE;
	if (rawLimit !== null) {
		limit = /^\d{1,4}$/.test(rawLimit) ? Number(rawLimit) : Number.NaN;
		if (!(limit >= 1 && limit <= MAX_PAGE)) return { error: { code: "invalid_limit", message: `limit must be between 1 and ${MAX_PAGE}` } };
	}
	if (rawBefore === null || rawBefore === "") return { page: { limit } };
	const before = cursorMessage(rawBefore);
	if (before === undefined) return { error: { code: "invalid_cursor", message: "Invalid cursor" } };
	return { page: { before, limit } };
}

/**
 * The view with only one page of its messages, and `earlierCursor` when earlier messages exist. A page never starts
 * with a tool result: it then also holds the reply that made the call (so it may hold a few more than `limit`). A
 * cursor whose message is no longer in the view (the conversation was compacted or reset) gives an empty page.
 */
export function pageView(view: ChatView, page: MessagePage): ChatView {
	const all = view.messages;
	const end = page.before === undefined ? all.length : all.findIndex((m) => m.id === page.before);
	if (end < 0) return { ...view, messages: [] };
	let start = Math.max(0, end - page.limit);
	while (start > 0 && all[start]?.role === "tool") start--;
	const { earlierCursor: _, ...rest } = view;
	return { ...rest, messages: all.slice(start, end), ...(start > 0 ? { earlierCursor: messageCursor(all[start]!.id) } : {}) };
}
