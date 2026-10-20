/*
 * Test: chat with the agent the way an app would. Its threads beside the open
 * conversation; messages go through the platform, which wakes a hibernated
 * agent first, and the conversation follows the agent's own transcript over
 * server-sent events. On a shared team's agent a new conversation is Shared
 * (everyone who can use the agent sees it) or Private (only its starter),
 * chosen with its first message; a personal workspace's are its owner's.
 * The thread list pages (and searches titles); the conversation shows its
 * latest messages and loads earlier ones on request. While a reply runs, a
 * message can steer it.
 */
import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { CornerDownRight, MessageSquarePlus, Search } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import {
  type Agent,
  agentsKey,
  type ChatMessage,
  deleteThread,
  earlierMessages,
  newId,
  sendMessage,
  stopThread,
  threadPagesQuery,
  threadProblem,
  threadsKey,
  type ThreadVisibility,
  useLiveThread,
  type WhenBusy,
} from "../../../api/agents";
import { errorMessage } from "../../../api/client";
import { useCurrentUser } from "../../../session";
import { useDebounced } from "../../admin/hooks";
import { markAwake } from "./labels";
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
import { Input } from "@/components/ui/input/input";
import {
  PromptInput,
  PromptInputButton,
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
  const [query, setQuery] = useState("");
  const q = useDebounced(query.trim());
  const threads = useInfiniteQuery({
    ...threadPagesQuery(agent.id, q),
    refetchInterval: 15_000,
  });
  const threadList = threads.data?.pages.flatMap((p) => p.threads);
  const [threadId, setThreadId] = useState<string | undefined>(undefined);
  // A new thread exists in the agent only after its first message.
  const [draftThread, setDraftThread] = useState(() => newId());
  const [visibility, setVisibility] = useState<ThreadVisibility>("shared");
  const [sending, setSending] = useState(false);
  // The composer's text (controlled, so a steering message can be sent from it while a reply runs).
  const [draft, setDraft] = useState("");

  // Open the most recent thread the first time the list loads (not after New chat).
  const opened = useRef(false);
  useEffect(() => {
    if (opened.current || !threadList) return;
    opened.current = true;
    if (threadId === undefined && threadList.length > 0) setThreadId(threadList[0]!.threadId);
  }, [threadList, threadId]);
  useEffect(() => preloadResponse(), []);

  const current = threadId ?? draftThread;
  const exists = threadId !== undefined;
  const thread = threadList?.find((t) => t.threadId === threadId);
  const live = useLiveThread(agent.id, current, exists);
  // Earlier messages loaded on request, for this thread and this snapshot of it.
  const [older, setOlder] = useState<{ key: string; messages: ChatMessage[]; cursor?: string | null }>();
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const olderKey = `${current}:${live.snapshots}`;
  const earlier = older?.key === olderKey ? older : undefined;
  const earlierCursor = earlier ? earlier.cursor : live.view?.earlierCursor;
  const view =
    live.view && earlier
      ? { ...live.view, messages: [...earlier.messages.filter((m) => !live.view!.messages.some((n) => n.id === m.id)), ...live.view.messages] }
      : live.view;
  const replying = Boolean(view?.busy);
  const status: ChatStatus = sending
    ? "submitted"
    : replying
      ? "streaming"
      : "ready";

  const refreshThreads = () =>
    queryClient.invalidateQueries({
      queryKey: threadsKey(agent.id),
    });

  const loadEarlier = async () => {
    if (!earlierCursor) return;
    setLoadingEarlier(true);
    try {
      const page = await earlierMessages(agent.id, current, earlierCursor);
      setOlder({ key: olderKey, messages: [...page.messages, ...(earlier?.messages ?? [])], cursor: page.earlierCursor ?? null });
    } catch (err) {
      toast.error("Couldn't load earlier messages", errorMessage(err));
    } finally {
      setLoadingEarlier(false);
    }
  };
  // An accepted message wakes the agent: show it awake right away, then take the platform's word.
  const refreshAgent = () => {
    markAwake(queryClient, agent.id);
    return queryClient.invalidateQueries({ queryKey: agentsKey });
  };

  /** Back to the list with a new conversation ready (the open one is gone or can't take the message). */
  const startNew = () => {
    setThreadId(undefined);
    setDraftThread(newId());
    setVisibility("shared");
  };

  const send = async (text: string, whenBusy?: WhenBusy) => {
    const trimmed = text.trim();
    if (!trimmed) return false;
    setSending(true);
    try {
      // Who sees it is chosen with the first message only; a personal workspace's are its owner's.
      await sendMessage(agent.id, current, trimmed, exists || personal ? undefined : visibility, whenBusy);
      setDraft((d) => (d === text ? "" : d));
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
        <Input
          type="search"
          size="sm"
          startIcon={<Search />}
          aria-label="Search chats"
          placeholder="Search chats"
          maxLength={200}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <ThreadList
          threads={threadList ?? []}
          activeId={threadId}
          viewerId={viewerId}
          personal={personal}
          onSelect={setThreadId}
          onDelete={(id) => void remove(id)}
        />
        {q && threadList?.length === 0 ? <p className={s.hint}>No chats match.</p> : null}
        {threads.hasNextPage ? (
          <Button variant="ghost" size="sm" block disabled={threads.isFetchingNextPage} onClick={() => void threads.fetchNextPage()}>
            {threads.isFetchingNextPage ? "Loading…" : "Load more chats"}
          </Button>
        ) : null}
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
            {earlierCursor ? (
              <Button variant="ghost" size="sm" disabled={loadingEarlier} onClick={() => void loadEarlier()}>
                {loadingEarlier ? "Loading…" : "Load earlier messages"}
              </Button>
            ) : null}
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
            // While a reply runs, Enter queues the message behind it (a follow-up); Steer joins it.
            submitWhileBusy={exists}
            onSubmit={(message) => send(message.text)}
          >
            <PromptInputTextarea
              placeholder={
                !agent.enabled
                  ? "This agent is turned off"
                  : replying
                    ? "Enter queues a follow-up; Steer joins the reply in progress"
                    : `Message ${agent.name}`
              }
              maxLength={32000}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
            <PromptInputToolbar>
              <PromptInputTools>
                {!exists && !personal ? (
                  <VisibilityPicker value={visibility} onChange={setVisibility} disabled={!agent.enabled || sending} />
                ) : null}
                {exists && replying ? (
                  // Joins the running reply after its current tool round (the model sees it before going on).
                  <PromptInputButton label="Steer" showLabel disabled={!draft.trim() || sending} onClick={() => void send(draft, "steer")}>
                    <CornerDownRight aria-hidden />
                  </PromptInputButton>
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
