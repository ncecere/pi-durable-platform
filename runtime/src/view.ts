import type { AssistantMessage, Message } from "@earendil-works/pi-ai";
import type { EntryRecord, LiveState } from "@earendil-works/pi-durable";

/** Who sent a user message, as the platform says (never as its client says). `via`: the app a person used. */
export type Author = { kind: "user" | "service_account"; id: string; name: string; via?: string };

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
	partial?: { text: string; thinking: string };
	/** Tool calls of the current round. */
	tools: { callId: string; name: string; status: string; output?: string }[];
};

export type ChatToolCall = { id: string; name: string; arguments: unknown; startedAt?: number; endedAt?: number };
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
	| { id: string; role: "tool"; toolCallId: string; name: string; text: string; isError: boolean; at?: number };

export function buildView(entries: readonly EntryRecord[], live: Readonly<LiveState> | undefined): ChatView {
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
			const converted = convert(id, message as Message, resultAt);
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
	const partialMessage = live?.generation?.message as AssistantMessage | undefined;
	return {
		busy: live?.run !== undefined,
		messages,
		...(partialMessage === undefined
			? {}
			: { partial: { text: textOf(partialMessage), thinking: thinkingOf(partialMessage) } }),
		tools: (live?.tools ?? []).map((slot) => ({
			callId: slot.callId,
			name: slot.name,
			status: slot.status,
			...(slot.output === undefined ? {} : { output: slot.output }),
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
		if ((kind === "user" || kind === "service_account") && typeof id === "string" && typeof name === "string") {
			out.author = { kind, id, name, ...(typeof via === "string" ? { via } : {}) };
		}
	}
	return out;
}

const timeOf = (message: { timestamp?: unknown }) => (typeof message.timestamp === "number" && message.timestamp > 0 ? message.timestamp : undefined);

function convert(id: string, message: Message, resultAt: ReadonlyMap<string, number>): ChatMessage | undefined {
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
				return [
					{
						id: block.id,
						name: block.name,
						arguments: block.arguments ?? {},
						...(calledAt === undefined ? {} : { startedAt: calledAt }),
						...(endedAt === undefined ? {} : { endedAt }),
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
			};
		default:
			return undefined;
	}
}

const textOf = (message: AssistantMessage) => message.content.flatMap((b) => (b.type === "text" ? [b.text] : [])).join("\n\n");
const thinkingOf = (message: AssistantMessage) =>
	message.content.flatMap((b) => (b.type === "thinking" && !b.redacted ? [b.thinking] : [])).join("\n\n");
