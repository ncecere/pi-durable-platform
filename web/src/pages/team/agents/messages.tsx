/*
 * A conversation's messages: user bubbles (with who sent them and when, on
 * threads with several people), assistant replies (Markdown) with a quiet
 * meta line, reasoning and tool calls (with how long each took, and their
 * approval when one was asked). Read-only, except that the test chat may
 * decide a pending approval (approvals); an admin's snapshot uses it too.
 */
import { Fragment } from "react";
import { type ChatMessage, type ChatView } from "../../../api/agents";
import { Message, MessageContent } from "@/components/ui/message/message";
import {
  Reasoning,
  ReasoningContent,
  ReasoningTrigger,
} from "@/components/ui/reasoning/reasoning";
import { LazyResponse } from "@/components/ui/response/response-lazy";
import { Time } from "@/components/ui/time/time";
import {
  Tool,
  ToolContent,
  ToolHeader,
  ToolInput,
  ToolOutput,
  type ToolState,
} from "@/components/ui/tool/tool";
import s from "./agents.module.css";
import { ApprovalNote } from "./approval-note";
import {
  authorName,
  manyPeople,
  metaText,
  type ReplyMeta,
  replyMetas,
  toolDuration,
} from "./message-meta";

/** Tool results by call ID, so a call shows with its result. */
function resultsById(messages: ChatMessage[]): Map<string, ChatMessage> {
  const out = new Map<string, ChatMessage>();
  for (const m of messages)
    if (m.role === "tool" && m.toolCallId) out.set(m.toolCallId, m);
  return out;
}

export type ChatMessagesProps = {
  view: ChatView;
  /** The person reading (their messages say "You"). */
  viewerId?: string;
  /** How many people have sent to the thread (its record's participants). */
  participants?: number;
  /** When the thread's last reply finished: the latest reply's duration. */
  lastReplyAt?: number | null;
  /** Lets the reader decide pending approvals (the agent, and whether they may decide each one). */
  approvals?: { agentId: string; canDecide: (approvalId: string) => boolean | undefined; onDecided?: () => void };
};

export function ChatMessages({ view, viewerId, participants, lastReplyAt, approvals }: ChatMessagesProps) {
  const results = resultsById(view.messages);
  const running = new Map(view.tools.map((t) => [t.callId, t]));
  const metas = replyMetas(view, lastReplyAt);
  const many = manyPeople(view.messages, participants);
  const note = (approval: NonNullable<ChatView["approvals"]>[number]) => (
    <ApprovalNote
      approval={approval}
      agentId={approvals?.agentId}
      canDecide={approvals?.canDecide(approval.id) ?? true}
      onDecided={approvals?.onDecided}
    />
  );
  // Pending approvals of calls the transcript doesn't show (a subagent's).
  const shown = new Set(view.messages.flatMap((m) => (m.toolCalls ?? []).map((c) => c.approval?.id)));
  const elsewhere = (view.approvals ?? []).filter((a) => !shown.has(a.id));
  return (
    <>
      {view.messages.map((m) => {
        if (m.role === "tool") return null;
        if (m.role === "user") {
          const name = authorName(m.author, viewerId, many);
          return (
            <Message key={m.id} from="user" label={name && name !== "You" ? `${name} said` : undefined}>
              <UserHeader name={name} via={m.author?.via} at={m.at} />
              <MessageContent>
                <p className={s.userText}>{m.text}</p>
              </MessageContent>
            </Message>
          );
        }
        const meta = metas.get(m.id);
        return (
          <Message key={m.id} from="assistant">
            <MessageContent>
              {m.thinking ? (
                <Reasoning>
                  <ReasoningTrigger />
                  <ReasoningContent>{m.thinking}</ReasoningContent>
                </Reasoning>
              ) : null}
              {(m.toolCalls ?? []).map((call) => {
                const result = results.get(call.id);
                const live = running.get(call.id);
                const state: ToolState = result
                  ? result.isError
                    ? "error"
                    : "completed"
                  : live?.status === "running"
                    ? "running"
                    : "pending";
                return (
                  <Fragment key={call.id}>
                    <Tool>
                      <ToolHeader name={call.name} state={state} summary={toolDuration(call)} />
                      <ToolContent>
                        <ToolInput input={call.arguments} />
                        {result ? (
                          <ToolOutput
                            output={result.isError ? undefined : result.text}
                            errorText={result.isError ? result.text : undefined}
                          />
                        ) : live?.output ? (
                          <ToolOutput
                            output={live.output}
                            label="Output so far"
                          />
                        ) : null}
                        <ToolDetails source={result ?? live} />
                      </ToolContent>
                    </Tool>
                    {call.approval ? note(call.approval) : null}
                  </Fragment>
                );
              })}
              {m.text ? <LazyResponse>{m.text}</LazyResponse> : null}
              {/* A stop the person asked for says "Stopped" in the meta line instead. */}
              {m.error && m.stopReason !== "aborted" ? <p className={s.replyError}>{m.error}</p> : null}
              {meta ? <ReplyMetaLine meta={meta} /> : null}
            </MessageContent>
          </Message>
        );
      })}
      {elsewhere.map((a) => (
        <Fragment key={a.id}>{note(a)}</Fragment>
      ))}
      {view.partial ? (
        <Message from="assistant">
          <MessageContent>
            {view.partial.thinking ? (
              <Reasoning streaming={!view.partial.text}>
                <ReasoningTrigger />
                <ReasoningContent>{view.partial.thinking}</ReasoningContent>
              </Reasoning>
            ) : null}
            {view.partial.text ? (
              <LazyResponse streaming>{view.partial.text}</LazyResponse>
            ) : null}
          </MessageContent>
        </Message>
      ) : null}
    </>
  );
}

/**
 * A tool's details: structured data it returned for apps (raw JSON; the tool
 * card is collapsed until opened). Too large ones are left out by the runtime.
 */
function ToolDetails({ source }: { source?: { details?: unknown; detailsOmitted?: boolean } }) {
  if (source?.detailsOmitted) return <p className={s.replyMeta}>Details too large to show</p>;
  if (source?.details === undefined || source.details === null) return null;
  return <ToolOutput output={source.details} label="Details" />;
}

/** Above a user message: who sent it (when shown), the app they used, and when. */
function UserHeader({ name, via, at }: { name?: string; via?: string; at?: number }) {
  if (!name && !via && at === undefined) return null;
  return (
    <p className={s.messageHeader}>
      {name ? <span className={s.author}>{name}</span> : null}
      {via ? <span>via {via}</span> : null}
      {at !== undefined ? <Time value={at} format="relative" /> : null}
    </p>
  );
}

function ReplyMetaLine({ meta }: { meta: ReplyMeta }) {
  const text = metaText(meta);
  if (!text) return null;
  return <p className={s.replyMeta}>{text}</p>;
}
