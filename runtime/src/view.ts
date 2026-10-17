import type { AssistantMessage, Message } from "@earendil-works/pi-ai";
import type { EntryRecord, LiveState } from "@earendil-works/pi-durable";

/** The chat view the platform relays to clients: transcript plus the reply in progress. */
export type ChatView = {
	busy: boolean;
	messages: ChatMessage[];
	/** The assistant reply being generated, if any. */
	partial?: { text: string; thinking: string };
	/** Tool calls of the current round. */
	tools: { callId: string; name: string; status: string; output?: string }[];
};

export type ChatMessage =
	| { id: string; role: "user"; text: string }
	| { id: string; role: "assistant"; text: string; thinking?: string; toolCalls?: { id: string; name: string; arguments: unknown }[]; error?: string }
	| { id: string; role: "tool"; toolCallId: string; name: string; text: string; isError: boolean };

export function buildView(entries: readonly EntryRecord[], live: Readonly<LiveState> | undefined): ChatView {
	const messages: ChatMessage[] = [];
	for (const entry of entries) {
		if (entry.kind === "pi.system" || entry.kind === "pi.compaction") continue;
		(entry.model ?? []).forEach((message, index) => {
			const id = index === 0 ? `e${entry.id}` : `e${entry.id}:${index}`;
			const converted = convert(id, message as Message);
			if (converted !== undefined) messages.push(converted);
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

function convert(id: string, message: Message): ChatMessage | undefined {
	switch (message.role) {
		case "user":
			return {
				id,
				role: "user",
				text: typeof message.content === "string" ? message.content : message.content.map((p) => (p.type === "text" ? p.text : "[image]")).join("\n"),
			};
		case "assistant": {
			const toolCalls = message.content.flatMap((block) =>
				block.type === "toolCall" ? [{ id: block.id, name: block.name, arguments: block.arguments ?? {} }] : [],
			);
			const thinking = thinkingOf(message);
			const error =
				message.stopReason === "aborted" ? "Interrupted" : message.stopReason === "error" ? (message.errorMessage ?? "The model request failed") : undefined;
			return {
				id,
				role: "assistant",
				text: textOf(message),
				...(thinking ? { thinking } : {}),
				...(toolCalls.length > 0 ? { toolCalls } : {}),
				...(error === undefined ? {} : { error }),
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
			};
		default:
			return undefined;
	}
}

const textOf = (message: AssistantMessage) => message.content.flatMap((b) => (b.type === "text" ? [b.text] : [])).join("\n\n");
const thinkingOf = (message: AssistantMessage) =>
	message.content.flatMap((b) => (b.type === "thinking" && !b.redacted ? [b.thinking] : [])).join("\n\n");
