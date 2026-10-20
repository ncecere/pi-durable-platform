/*
 * Agents: queries, and a live view of one conversation over server-sent
 * events. The platform relays thread routes to the agent's runtime, waking it
 * first; the runtime is the source of truth for the transcript.
 */
import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { applyChatDelta, type ChatDeltaEvent, deltaEventTypes } from "./chat-delta";
import { ApiError, api, ifMatch, type Schemas, unwrap } from "./client";

export type Agent = Schemas["Agent"];
export type ChatView = Schemas["ChatView"];
export type ChatMessage = Schemas["ChatMessage"];
export type ChatAuthor = Schemas["ChatAuthor"];
export type AgentThread = Schemas["AgentThread"];
export type AgentThreadEvent = Schemas["AgentThreadEvent"];
export type AdminAgentThread = Schemas["AdminAgentThread"];
export type ThreadVisibility = Schemas["ThreadVisibility"];

export const agentsKey = ["agents"];

/** The agents of the person's teams (with service-managed ones where they are an owner or admin). */
export const agentsQuery = () =>
  queryOptions({
    queryKey: [...agentsKey, "list"],
    queryFn: async () => unwrap(await api.GET("/v1/agents", { params: { query: { includeManaged: true } } })),
  });

/** A team's agents (service-managed ones too, for its owners and admins). */
export const teamAgentsQuery = (team: string) =>
  queryOptions({
    queryKey: [...agentsKey, "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/agents", { params: { path: { team }, query: { includeManaged: true } } })),
  });

export const agentQuery = (agentId: string) =>
  queryOptions({
    queryKey: [...agentsKey, agentId],
    queryFn: async () => unwrap(await api.GET("/v1/agents/{agentId}", { params: { path: { agentId } } })),
  });

export type AgentMovePlan = Schemas["AgentMovePlan"];

/** What moving the agent to the team would change, and what stops it (a dry run; authorized like the move). */
export const movePlanQuery = (agentId: string, team: string) =>
  queryOptions({
    queryKey: [...agentsKey, agentId, "move", team],
    queryFn: async () => unwrap(await api.GET("/v1/agents/{agentId}/move", { params: { path: { agentId }, query: { team } } })),
    staleTime: 0,
  });

/** Moves the agent, with its conversations, to the team (If-Match on its revision). */
export async function moveAgent(agent: Agent, team: string): Promise<Agent> {
  return unwrap(
    await api.POST("/v1/agents/{agentId}/move", {
      params: { path: { agentId: agent.id }, header: ifMatch(agent.revision) },
      body: { team },
    }),
  );
}

export const templatesQuery = () =>
  queryOptions({ queryKey: ["agent-templates"], queryFn: async () => unwrap(await api.GET("/v1/agent-templates")), staleTime: 5 * 60_000 });

/** The key of an agent's thread lists (every search and page). */
export const threadsKey = (agentId: string) => [...agentsKey, agentId, "threads"];

export const THREAD_PAGE = 50;

/** An agent's threads, newest activity first, a page at a time; `q` filters titles. */
export const threadPagesQuery = (agentId: string, q: string) =>
  infiniteQueryOptions({
    queryKey: [...threadsKey(agentId), { q }],
    queryFn: async ({ pageParam }) => {
      const res = await api.GET("/v1/agents/{agentId}/threads", {
        params: { path: { agentId }, query: { limit: THREAD_PAGE, cursor: pageParam, q: q || undefined } },
      });
      const threads = unwrap(res);
      return { threads, nextCursor: res.data?.nextCursor };
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });

/** Messages per page of a conversation (the live window, and each earlier page). */
export const MESSAGE_PAGE = 50;

/** The page of a conversation's messages before `before` (an earlierCursor). */
export async function earlierMessages(agentId: string, threadId: string, before: string): Promise<ChatView> {
  return unwrap(
    await api.GET("/v1/agents/{agentId}/threads/{threadId}", {
      params: { path: { agentId, threadId }, query: { before, limit: MESSAGE_PAGE } },
    }),
  );
}

/** While a reply runs: followUp waits for it to end; steer joins it after its current tool round. */
export type WhenBusy = "followUp" | "steer";

/**
 * Sends a message. `visibility` goes with a thread's first message only (it
 * never changes afterwards; the platform refuses a different one).
 */
export async function sendMessage(agentId: string, threadId: string, text: string, visibility?: ThreadVisibility, whenBusy?: WhenBusy): Promise<void> {
  unwrap(
    await api.POST("/v1/agents/{agentId}/threads/{threadId}/messages", {
      params: { path: { agentId, threadId } },
      body: { text, requestId: newId(), ...(visibility ? { visibility } : {}), ...(whenBusy ? { whenBusy } : {}) },
    }),
  );
}

/**
 * A send that failed because of the thread itself: its visibility was set by
 * its first message (409 visibility_fixed), or it's gone or someone else's
 * private thread (404 thread_not_found).
 */
export function threadProblem(err: unknown): "visibility_fixed" | "thread_not_found" | undefined {
  if (!(err instanceof ApiError)) return undefined;
  if (err.status === 409 && err.code === "visibility_fixed") return "visibility_fixed";
  if (err.status === 404 && err.code === "thread_not_found") return "thread_not_found";
  return undefined;
}

/** Every thread of an agent, private ones too, for platform admins (titles of private and personal ones withheld). */
export const adminThreadsQuery = (agentId: string) =>
  queryOptions({
    queryKey: ["admin", "agents", agentId, "threads"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/agents/{agentId}/threads", { params: { path: { agentId } } })),
  });

/** A platform admin's reasoned read of one conversation: recorded in the agent's team's audit log, returned once. */
export async function adminReadThread(agentId: string, threadId: string, reason: string): Promise<ChatView> {
  return unwrap(
    await api.POST("/v1/admin/agents/{agentId}/threads/{threadId}/read", {
      params: { path: { agentId, threadId } },
      body: { reason },
    }),
  );
}

export async function stopThread(agentId: string, threadId: string): Promise<void> {
  unwrap(await api.POST("/v1/agents/{agentId}/threads/{threadId}/stop", { params: { path: { agentId, threadId } } }));
}

export async function deleteThread(agentId: string, threadId: string): Promise<void> {
  unwrap(await api.DELETE("/v1/agents/{agentId}/threads/{threadId}", { params: { path: { agentId, threadId } } }));
}

/** A client-chosen ID for a thread or a request (letters and digits). */
export function newId(): string {
  return crypto.randomUUID().replaceAll("-", "");
}

/**
 * The live view: the latest messages (`view.earlierCursor` pages earlier ones)
 * and the reply in progress. `snapshots` counts the snapshots received: each
 * one replaces the view (earlier pages loaded before it may no longer join on).
 */
export type LiveThread = { view: ChatView | undefined; connected: boolean; snapshots: number };

/**
 * Follows a thread over its delta stream: a snapshot of the latest
 * MESSAGE_PAGE messages, then only changes (new tokens, not the whole
 * transcript). EventSource reconnects by itself (for example while a
 * hibernated agent wakes up) and every connection starts with a snapshot, so
 * nothing is lost across reconnects. `enabled` is false for a thread that has
 * no messages yet (the runtime answers 404 until the first one).
 */
export function useLiveThread(agentId: string, threadId: string, enabled: boolean): LiveThread {
  const [state, setState] = useState<LiveThread>({ view: undefined, connected: false, snapshots: 0 });
  useEffect(() => {
    setState({ view: undefined, connected: false, snapshots: 0 });
    if (!enabled) return;
    const source = new EventSource(
      `/v1/agents/${encodeURIComponent(agentId)}/threads/${encodeURIComponent(threadId)}/events?format=delta&v=1&limit=${MESSAGE_PAGE}`,
      { withCredentials: true },
    );
    const apply = (event: Event) => {
      let delta: ChatDeltaEvent;
      try {
        delta = JSON.parse((event as MessageEvent<string>).data) as ChatDeltaEvent;
      } catch {
        return;
      }
      setState((s) => ({
        view: applyChatDelta(s.view, delta),
        connected: true,
        snapshots: s.snapshots + (delta.type === "snapshot" ? 1 : 0),
      }));
    };
    for (const type of deltaEventTypes) source.addEventListener(type, apply);
    source.onerror = () => setState((s) => ({ ...s, connected: false }));
    return () => source.close();
  }, [agentId, threadId, enabled]);
  return state;
}

/**
 * Follows one agent's threads over `GET /v1/agents/events`: `onEvent` gets each thread event (created, renamed,
 * replying, finished, deleted), and "ready" when the stream opens or reopens (it may have missed some).
 * `enabled` is false for someone who can't use the agent (the platform refuses the stream). A thread replying
 * means the agent's runtime is up, so the agent page uses this to keep its status badge current.
 */
export function useAgentEvents(agentId: string, enabled: boolean, onEvent: (event: AgentThreadEvent | "ready") => void) {
  const latest = useRef(onEvent);
  useEffect(() => {
    latest.current = onEvent;
  });
  useEffect(() => {
    if (!enabled) return;
    const source = new EventSource(`/v1/agents/events?agentId=${encodeURIComponent(agentId)}`, { withCredentials: true });
    source.addEventListener("ready", () => latest.current("ready"));
    source.addEventListener("thread", (event) => {
      try {
        latest.current(JSON.parse((event as MessageEvent<string>).data) as AgentThreadEvent);
      } catch {
        // A malformed event is skipped; the next one or the poll catches up.
      }
    });
    return () => source.close();
  }, [agentId, enabled]);
}
