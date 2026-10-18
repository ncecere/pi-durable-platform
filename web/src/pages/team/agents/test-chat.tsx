/*
 * Test: chat with the agent the way an app would. Its threads beside the open
 * conversation; messages go through the platform, which wakes a hibernated
 * agent first, and the conversation follows the agent's own transcript over
 * server-sent events. On a shared team's agent a new conversation is Shared
 * (everyone who can use the agent sees it) or Private (only its starter),
 * chosen with its first message; a personal workspace's are its owner's.
 */
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { MessageSquarePlus } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  type Agent,
  agentsKey,
  deleteThread,
  newId,
  sendMessage,
  stopThread,
  threadProblem,
  threadsQuery,
  type ThreadVisibility,
  useLiveThread,
} from "../../../api/agents";
import { errorMessage } from "../../../api/client";
import { useCurrentUser } from "../../../session";
import { ChatMessages } from "./messages";
import { ThreadAudience, ThreadList } from "./thread-list";
import { VisibilityPicker } from "./visibility-picker";
import { Button } from "@/components/ui/button/button";
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
  PromptInputTools,
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

/** What a send refused because of the thread itself says. */
const problemToasts = {
  visibility_fixed: ["This conversation's visibility can't change", "Its first message set who sees it. Start a new chat to choose again."],
  thread_not_found: ["This conversation isn't available", "It was deleted, or it's someone else's private conversation."],
} as const;

type Props = {
  agent: Agent;
  /** The agent is in a personal workspace: no one to share a conversation with. */
  personal?: boolean;
};

export function TestChat({ agent, personal = false }: Props) {
  const queryClient = useQueryClient();
  const viewerId = useCurrentUser().user.id;
  const threads = useQuery({
    ...threadsQuery(agent.id),
    refetchInterval: 15_000,
  });
  const [threadId, setThreadId] = useState<string | undefined>(undefined);
  // A new thread exists in the agent only after its first message.
  const [draftThread, setDraftThread] = useState(() => newId());
  const [visibility, setVisibility] = useState<ThreadVisibility>("shared");
  const [sending, setSending] = useState(false);

  // Open the most recent thread the first time the list loads (not after New chat).
  const opened = useRef(false);
  useEffect(() => {
    if (opened.current || !threads.data) return;
    opened.current = true;
    if (threadId === undefined && threads.data.length > 0) setThreadId(threads.data[0]!.threadId);
  }, [threads.data, threadId]);
  useEffect(() => preloadResponse(), []);

  const current = threadId ?? draftThread;
  const exists = threadId !== undefined;
  const thread = threads.data?.find((t) => t.threadId === threadId);
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

  /** Back to the list with a new conversation ready (the open one is gone or can't take the message). */
  const startNew = () => {
    setThreadId(undefined);
    setDraftThread(newId());
    setVisibility("shared");
  };

  const send = async (text: string) => {
    const trimmed = text.trim();
    if (!trimmed) return false;
    setSending(true);
    try {
      // Who sees it is chosen with the first message only; a personal workspace's are its owner's.
      await sendMessage(agent.id, current, trimmed, exists || personal ? undefined : visibility);
      if (!exists) {
        setThreadId(current);
        setDraftThread(newId());
      }
      void refreshThreads();
      void refreshAgent();
      return true;
    } catch (err) {
      const problem = threadProblem(err);
      if (problem) {
        const [title, description] = problemToasts[problem];
        toast.error(title, description);
        startNew();
        void refreshThreads();
        return false;
      }
      toast.error("Couldn't send the message", errorMessage(err));
      return false;
    } finally {
      setSending(false);
    }
  };

  const remove = async (id: string) => {
    try {
      await deleteThread(agent.id, id);
      if (id === threadId) startNew();
      await refreshThreads();
    } catch (err) {
      toast.error("Couldn't delete the chat", errorMessage(err));
    }
  };

  return (
    <div className={s.chat}>
      <nav className={s.threads} aria-label="Chats">
        <Button variant="secondary" size="sm" block onClick={startNew}>
          <MessageSquarePlus aria-hidden /> New chat
        </Button>
        <ThreadList
          threads={threads.data ?? []}
          activeId={threadId}
          viewerId={viewerId}
          personal={personal}
          onSelect={setThreadId}
          onDelete={(id) => void remove(id)}
        />
        {threads.isError ? (
          <p className={s.hint}>{errorMessage(threads.error)}</p>
        ) : null}
      </nav>

      <section className={s.pane} aria-label={agent.name}>
        <header className={s.paneHeader}>
          <span className={s.paneTitle}>
            {exists ? (thread?.title ?? "Conversation") : "New test conversation"}
          </span>
          {exists && !live.connected ? (
            <span className={s.hint}>Connecting…</span>
          ) : null}
          {thread && !personal ? <ThreadAudience thread={thread} viewerId={viewerId} /> : null}
        </header>
        <Conversation className={s.conversation}>
          <ConversationContent>
            {view && view.messages.length > 0 ? (
              <ChatMessages
                view={view}
                viewerId={viewerId}
                participants={thread?.participants.length}
                lastReplyAt={thread?.lastReplyAt}
              />
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
              <PromptInputTools>
                {!exists && !personal ? (
                  <VisibilityPicker value={visibility} onChange={setVisibility} disabled={!agent.enabled || sending} />
                ) : null}
              </PromptInputTools>
              <PromptInputSubmit />
            </PromptInputToolbar>
          </PromptInput>
        </div>
      </section>
    </div>
  );
}
