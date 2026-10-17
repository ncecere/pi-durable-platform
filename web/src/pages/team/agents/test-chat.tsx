/*
 * Test: chat with the agent the way an app would. Its threads beside the open
 * conversation; messages go through the platform, which wakes a hibernated
 * agent first, and the conversation follows the agent's own transcript over
 * server-sent events. Test threads are shared with the team.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { MessageSquarePlus, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";
import {
  type Agent,
  agentsKey,
  deleteThread,
  newId,
  sendMessage,
  stopThread,
  threadsQuery,
  useLiveThread,
} from "../../../api/agents";
import { errorMessage } from "../../../api/client";
import { ChatMessages } from "./messages";
import { Button, IconButton } from "@/components/ui/button/button";
import {
  type ChatStatus,
  Conversation,
  ConversationAnnouncer,
  ConversationContent,
  ConversationEmptyState,
  ConversationScrollButton,
} from "@/components/ui/conversation/conversation";
import {
  PromptInput,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputToolbar,
} from "@/components/ui/prompt-input/prompt-input";
import { preloadResponse } from "@/components/ui/response/response-lazy";
import { Suggestion, Suggestions } from "@/components/ui/suggestion/suggestion";
import { toast } from "@/components/ui/toast/toast";
import s from "./agents.module.css";

const SUGGESTIONS = [
  "What time is it?",
  "What can you help me with?",
  "Summarize what we talked about",
];

export function TestChat({ agent }: { agent: Agent }) {
  const queryClient = useQueryClient();
  const threads = useQuery({
    ...threadsQuery(agent.id),
    refetchInterval: 15_000,
  });
  const [threadId, setThreadId] = useState<string | undefined>(undefined);
  // A new thread exists in the agent only after its first message.
  const [draftThread, setDraftThread] = useState(() => newId());
  const [sending, setSending] = useState(false);

  // Open the most recent thread the first time the list loads.
  useEffect(() => {
    if (threadId === undefined && threads.data && threads.data.length > 0)
      setThreadId(threads.data[0]!.threadId);
  }, [threads.data, threadId]);
  useEffect(() => preloadResponse(), []);

  const current = threadId ?? draftThread;
  const exists = threadId !== undefined;
  const live = useLiveThread(agent.id, current, exists);
  const view = live.view;
  const status: ChatStatus = sending
    ? "submitted"
    : view?.busy
      ? "streaming"
      : "ready";

  const refreshThreads = () =>
    queryClient.invalidateQueries({
      queryKey: threadsQuery(agent.id).queryKey,
    });
  // Sending wakes the agent: refresh its status right away.
  const refreshAgent = () =>
    queryClient.invalidateQueries({ queryKey: agentsKey });

  const send = async (text: string) => {
    const trimmed = text.trim();
    if (!trimmed) return false;
    setSending(true);
    try {
      await sendMessage(agent.id, current, trimmed);
      if (!exists) {
        setThreadId(current);
        setDraftThread(newId());
      }
      void refreshThreads();
      void refreshAgent();
      return true;
    } catch (err) {
      toast.error("Couldn't send the message", errorMessage(err));
      return false;
    } finally {
      setSending(false);
    }
  };

  const remove = async (id: string) => {
    try {
      await deleteThread(agent.id, id);
      if (id === threadId) setThreadId(undefined);
      await refreshThreads();
    } catch (err) {
      toast.error("Couldn't delete the chat", errorMessage(err));
    }
  };

  return (
    <div className={s.chat}>
      <nav className={s.threads} aria-label="Chats">
        <Button
          variant="secondary"
          size="sm"
          block
          onClick={() => {
            setThreadId(undefined);
            setDraftThread(newId());
          }}
        >
          <MessageSquarePlus aria-hidden /> New chat
        </Button>
        <ul className={s.threadList}>
          {(threads.data ?? []).map((t) => (
            <li
              key={t.threadId}
              className={s.threadRow}
              data-active={t.threadId === threadId || undefined}
            >
              <button
                type="button"
                className={s.threadButton}
                onClick={() => setThreadId(t.threadId)}
                aria-current={t.threadId === threadId || undefined}
              >
                {t.title}
              </button>
              <IconButton
                icon={<Trash2 aria-hidden />}
                label={`Delete chat “${t.title}”`}
                size="sm"
                onClick={() => void remove(t.threadId)}
              />
            </li>
          ))}
        </ul>
        {threads.isError ? (
          <p className={s.hint}>{errorMessage(threads.error)}</p>
        ) : null}
      </nav>

      <section className={s.pane} aria-label={agent.name}>
        <header className={s.paneHeader}>
          <span className={s.paneTitle}>
            {exists
              ? (threads.data?.find((t) => t.threadId === threadId)?.title ??
                "Conversation")
              : "New test conversation"}
          </span>
          {exists && !live.connected ? (
            <span className={s.hint}>Connecting…</span>
          ) : null}
          <span className={s.hint}>Shared with the team</span>
        </header>
        <Conversation className={s.conversation}>
          <ConversationContent>
            {view && view.messages.length > 0 ? (
              <ChatMessages view={view} />
            ) : (
              <ConversationEmptyState
                title={`Test ${agent.name}`}
                description="Send a message as an app would. Conversations are durable: they survive reloads, restarts and hibernation."
              >
                <Suggestions>
                  {SUGGESTIONS.map((text) => (
                    <Suggestion
                      key={text}
                      suggestion={text}
                      onSelect={(t) => void send(t)}
                    />
                  ))}
                </Suggestions>
              </ConversationEmptyState>
            )}
          </ConversationContent>
          <ConversationScrollButton />
        </Conversation>
        <ConversationAnnouncer status={status} />
        <div className={s.composer}>
          <PromptInput
            status={status}
            disabled={!agent.enabled}
            onStop={
              exists
                ? () =>
                    void stopThread(agent.id, current).catch((err) =>
                      toast.error("Couldn't stop", errorMessage(err)),
                    )
                : undefined
            }
            onSubmit={(message) => send(message.text)}
          >
            <PromptInputTextarea
              placeholder={
                agent.enabled
                  ? `Message ${agent.name}`
                  : "This agent is turned off"
              }
              maxLength={32000}
            />
            <PromptInputToolbar>
              <span />
              <PromptInputSubmit />
            </PromptInputToolbar>
          </PromptInput>
        </div>
      </section>
    </div>
  );
}
