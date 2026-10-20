import type { AssistantMessage, Message, ToolResultMessage } from "@earendil-works/pi-ai";
import { BACKGROUND_CONTEXT } from "@earendil-works/chord/context";
import { type ConversationId, type EntryRecord, type Harness, LiveDoc } from "@earendil-works/pi-durable";
import type { TestAgent } from "./agent.ts";

const ctx = BACKGROUND_CONTEXT;

/** Thrown by `expectReplayed` with every problem found. */
export class ReplayAssertionError extends Error {
	constructor(readonly problems: string[]) {
		super(`the replayed run repeated or lost work:\n- ${problems.join("\n- ")}`);
		this.name = "ReplayAssertionError";
	}
}

export type ReplaySummary = {
	/** Tool calls in the thread (subagents' calls not included). */
	toolCalls: number;
	/** Subagents (results whose details name a child conversation), each checked recursively. */
	subagents: number;
};

/**
 * Checks that a thread's run, after any number of crashes and restarts, ended once and neither repeated nor lost
 * anything: every tool call the model made has exactly one result (none missing, none twice, none for a call that
 * wasn't made); no result says the call was interrupted, abandoned or aborted (unless `allowInterrupted`: a tool that
 * isn't replay-safe is meant to end that way); the run is over and ends with an answer; and every subagent (a tool
 * result whose `details.conversationId` names a child conversation) was given its task once and its answer reached
 * the parent's result, checked the same way inside the child. Throws `ReplayAssertionError` listing every problem.
 */
export async function expectReplayed(agent: TestAgent | { harness: Harness; threads: { get(threadId: string): Promise<{ id: ConversationId } | undefined> } }, threadId: string, options: { allowInterrupted?: boolean } = {}): Promise<ReplaySummary> {
	const harness = agent.harness;
	const threads = "agent" in agent ? agent.agent.threads : agent.threads;
	const conversation = await threads.get(threadId);
	if (conversation === undefined) throw new ReplayAssertionError([`thread ${threadId} doesn't exist`]);
	const problems: string[] = [];
	const summary = await check(harness, conversation.id, `thread ${threadId}`, options, problems, true);
	if (problems.length > 0) throw new ReplayAssertionError(problems);
	return summary;
}

type Call = { id: string; name: string };
type Result = { message: ToolResultMessage; codes: string[] };

async function entriesOf(harness: Harness, id: ConversationId): Promise<readonly EntryRecord[]> {
	const conversation = await harness.conversation(id, ctx);
	if (conversation === undefined) return [];
	const watch = await conversation.watch(ctx);
	const entries = watch.value.entries;
	await watch.stop();
	return entries;
}

async function check(harness: Harness, id: ConversationId, where: string, options: { allowInterrupted?: boolean }, problems: string[], top: boolean): Promise<ReplaySummary> {
	const entries = await entriesOf(harness, id);
	const calls: Call[] = [];
	const results = new Map<string, Result[]>();
	const users: string[] = [];
	let last: Message | undefined;
	for (const entry of entries) {
		if (entry.kind === "pi.system" || entry.kind === "pi.compaction") continue;
		for (const message of (entry.model ?? []) as Message[]) {
			last = message;
			if (message.role === "user") users.push(typeof message.content === "string" ? message.content : message.content.map((c) => (c.type === "text" ? c.text : "")).join(""));
			if (message.role === "assistant") for (const block of message.content) if (block.type === "toolCall") calls.push({ id: block.id, name: block.name });
			if (message.role === "toolResult") {
				const diagnostics = (entry.data as { diagnostics?: { code?: string }[] } | undefined)?.diagnostics ?? [];
				const codes = diagnostics.flatMap((d) => (typeof d.code === "string" ? [d.code] : []));
				results.set(message.toolCallId, [...(results.get(message.toolCallId) ?? []), { message, codes }]);
			}
		}
	}

	const seen = new Set<string>();
	for (const call of calls) {
		if (seen.has(call.id)) problems.push(`${where}: the model's tool call ${call.id} (${call.name}) appears twice`);
		seen.add(call.id);
		const got = results.get(call.id) ?? [];
		if (got.length === 0) problems.push(`${where}: tool call ${call.id} (${call.name}) has no result (lost)`);
		if (got.length > 1) problems.push(`${where}: tool call ${call.id} (${call.name}) has ${got.length} results (repeated)`);
		const ended = got[0]?.codes.find((code) => code === "interrupted" || code === "abandoned" || code === "aborted");
		if (ended !== undefined && !options.allowInterrupted) problems.push(`${where}: tool call ${call.id} (${call.name}) ended ${ended} (lost)`);
	}
	for (const callId of results.keys()) if (!seen.has(callId)) problems.push(`${where}: a result for tool call ${callId}, which the model never made`);

	const live = await harness.snapshot(LiveDoc, id, ctx);
	if (live?.run !== undefined) problems.push(`${where}: the run hasn't ended`);
	const final = last?.role === "assistant" ? (last as AssistantMessage) : undefined;
	if (final === undefined || final.content.some((block) => block.type === "toolCall")) problems.push(`${where}: doesn't end with an answer`);
	if (!top && users.length !== 1) problems.push(`${where}: was given its task ${users.length} times (expected once)`);

	let subagents = 0;
	for (const [callId, got] of results) {
		const child = (got[0]?.message.details as { conversationId?: unknown } | undefined)?.conversationId;
		if (typeof child !== "number") continue;
		subagents++;
		const childWhere = `subagent of call ${callId} (conversation ${child})`;
		const nested = await check(harness, child as ConversationId, childWhere, options, problems, false);
		subagents += nested.subagents;
		const answerText = await finalText(harness, child as ConversationId);
		const resultText = got[0]!.message.content.map((c) => (c.type === "text" ? c.text : "")).join("");
		if (answerText === undefined || !resultText.includes(answerText)) problems.push(`${childWhere}: its answer didn't reach the parent's result (lost)`);
	}
	return { toolCalls: calls.length, subagents };
}

async function finalText(harness: Harness, id: ConversationId): Promise<string | undefined> {
	const entries = await entriesOf(harness, id);
	for (let i = entries.length - 1; i >= 0; i--) {
		const message = (entries[i]!.model ?? []).at(-1) as Message | undefined;
		if (message?.role === "assistant") return message.content.flatMap((c) => (c.type === "text" ? [c.text] : [])).join("");
	}
	return undefined;
}
