/* A conversation's messages: user bubbles, assistant replies (Markdown), reasoning and tool calls. */
import { type ChatMessage, type ChatView } from "../../api/agents";
import { Message, MessageContent } from "@/components/ui/message/message";
import { Reasoning, ReasoningContent, ReasoningTrigger } from "@/components/ui/reasoning/reasoning";
import { LazyResponse } from "@/components/ui/response/response-lazy";
import { Tool, ToolContent, ToolHeader, ToolInput, ToolOutput, type ToolState } from "@/components/ui/tool/tool";
import s from "./agents.module.css";

/** Tool results by call ID, so a call shows with its result. */
function resultsById(messages: ChatMessage[]): Map<string, ChatMessage> {
  const out = new Map<string, ChatMessage>();
  for (const m of messages) if (m.role === "tool" && m.toolCallId) out.set(m.toolCallId, m);
  return out;
}

export function ChatMessages({ view }: { view: ChatView }) {
  const results = resultsById(view.messages);
  const running = new Map(view.tools.map((t) => [t.callId, t]));
  return (
    <>
      {view.messages.map((m) => {
        if (m.role === "tool") return null;
        if (m.role === "user") {
          return (
            <Message key={m.id} from="user">
              <MessageContent>
                <p className={s.userText}>{m.text}</p>
              </MessageContent>
            </Message>
          );
        }
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
                const state: ToolState = result ? (result.isError ? "error" : "completed") : live?.status === "running" ? "running" : "pending";
                return (
                  <Tool key={call.id}>
                    <ToolHeader name={call.name} state={state} />
                    <ToolContent>
                      <ToolInput input={call.arguments} />
                      {result ? (
                        <ToolOutput output={result.isError ? undefined : result.text} errorText={result.isError ? result.text : undefined} />
                      ) : live?.output ? (
                        <ToolOutput output={live.output} label="Output so far" />
                      ) : null}
                    </ToolContent>
                  </Tool>
                );
              })}
              {m.text ? <LazyResponse>{m.text}</LazyResponse> : null}
              {m.error ? <p className={s.replyError}>{m.error}</p> : null}
            </MessageContent>
          </Message>
        );
      })}
      {view.partial ? (
        <Message from="assistant">
          <MessageContent>
            {view.partial.thinking ? (
              <Reasoning streaming={!view.partial.text}>
                <ReasoningTrigger />
                <ReasoningContent>{view.partial.thinking}</ReasoningContent>
              </Reasoning>
            ) : null}
            {view.partial.text ? <LazyResponse streaming>{view.partial.text}</LazyResponse> : null}
          </MessageContent>
        </Message>
      ) : null}
    </>
  );
}
