/*
 * What a conversation shows around its messages, from the chat view's
 * metadata: who sent a user message, and a quiet line under each reply
 * (model · tokens · duration · how it stopped). Older entries carry less;
 * whatever is missing is left out.
 */
import type { ChatAuthor, ChatMessage, ChatView } from "../../../api/agents";
import { tokens } from "../../../lib/format";

/** "0.4s", "12s", "1m 5s", "1h 2m". */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  if (ms < 10_000) return `${(Math.round(ms / 100) / 10).toLocaleString()}s`;
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
}

/** How a reply ended, when that isn't a normal stop. An error is shown with the reply already. */
const stopLabels: Record<string, string> = { length: "Length limit", aborted: "Stopped" };
export const stopLabel = (reason: string | undefined) => (reason ? stopLabels[reason] : undefined);

/** A reply's meta line: the model, its tokens, how long it took and how it stopped (each when known). */
export type ReplyMeta = { model?: string; tokens?: number; durationMs?: number; stop?: string };

/** The meta line in words: "claude-sonnet-4 · 1,234 tokens · 3.2s · Length limit". */
export function metaText(meta: ReplyMeta): string {
  const parts = [
    meta.model,
    meta.tokens ? `${tokens(meta.tokens)} tokens` : undefined,
    meta.durationMs !== undefined ? formatDuration(meta.durationMs) : undefined,
    meta.stop,
  ];
  return parts.filter(Boolean).join(" · ");
}

/**
 * The meta line of each reply, keyed by its last assistant message. A reply is
 * the assistant messages (and tool steps) after a user message; its tokens add
 * up. Its duration runs from its first message to its end: when its last step
 * called tools, or for the latest reply when the thread says it finished
 * (`lastReplyAt`). A reply still in progress has none.
 */
export function replyMetas(view: Pick<ChatView, "messages" | "busy">, lastReplyAt?: number | null): Map<string, ReplyMeta> {
  const out = new Map<string, ReplyMeta>();
  const turns: ChatMessage[][] = [];
  let turn: ChatMessage[] = [];
  for (const m of view.messages) {
    if (m.role === "user") {
      if (turn.length) turns.push(turn);
      turn = [];
    } else if (m.role === "assistant") turn.push(m);
  }
  if (turn.length) turns.push(turn);
  turns.forEach((replies, i) => {
    const latest = i === turns.length - 1;
    if (latest && view.busy) return;
    const first = replies[0]!;
    const last = replies[replies.length - 1]!;
    const total = replies.reduce((n, m) => n + (m.usage?.total ?? 0), 0);
    const end = latest && lastReplyAt ? lastReplyAt : last.toolCalls?.find((c) => c.startedAt)?.startedAt;
    const durationMs = first.at !== undefined && end !== undefined && end >= first.at ? end - first.at : undefined;
    out.set(last.id, {
      model: last.model?.id ?? replies.findLast((m) => m.model)?.model?.id,
      tokens: total || undefined,
      durationMs,
      stop: stopLabel(last.stopReason),
    });
  });
  return out;
}

/** Everyone who sent a message in the view (by author ID). */
const authorsIn = (messages: ChatMessage[]) => new Set(messages.flatMap((m) => (m.role === "user" && m.author ? [m.author.id] : [])));

/**
 * Whether user messages say who sent them: on threads with more than one
 * participant (from the thread's record, else the view's authors). On others
 * only messages from someone other than the viewer do.
 */
export function manyPeople(messages: ChatMessage[], participants?: number): boolean {
  return Math.max(participants ?? 0, authorsIn(messages).size) > 1;
}

/** The sender's name above a user message ("You" for the viewer), or undefined when it isn't shown. */
export function authorName(author: ChatAuthor | undefined, viewerId: string | undefined, many: boolean): string | undefined {
  if (!author) return undefined;
  const mine = author.kind === "user" && author.id === viewerId;
  if (mine) return many ? "You" : undefined;
  return author.name;
}

/** A tool step's duration ("1.2s"), once its result arrived. */
export function toolDuration(call: { startedAt?: number; endedAt?: number }): string | undefined {
  if (call.startedAt === undefined || call.endedAt === undefined || call.endedAt < call.startedAt) return undefined;
  return formatDuration(call.endedAt - call.startedAt);
}
