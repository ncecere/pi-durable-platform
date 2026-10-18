/*
 * Agents: queries, and a live view of one conversation over server-sent
 * events. The platform relays thread routes to the agent's runtime, waking it
 * first; the runtime is the source of truth for the transcript.
 */
import { queryOptions } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { ApiError, api, type Schemas, unwrap } from "./client";

export type Agent = Schemas["Agent"];
export type ChatView = Schemas["ChatView"];
export type ChatMessage = Schemas["ChatMessage"];
export type ChatAuthor = Schemas["ChatAuthor"];
export type AgentThread = Schemas["AgentThread"];
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

export const templatesQuery = () =>
  queryOptions({ queryKey: ["agent-templates"], queryFn: async () => unwrap(await api.GET("/v1/agent-templates")), staleTime: 5 * 60_000 });

export const threadsQuery = (agentId: string) =>
  queryOptions({
    queryKey: [...agentsKey, agentId, "threads"],
    queryFn: async () => unwrap(await api.GET("/v1/agents/{agentId}/threads", { params: { path: { agentId } } })),
  });

/**
 * Sends a message. `visibility` goes with a thread's first message only (it
 * never changes afterwards; the platform refuses a different one).
 */
export async function sendMessage(agentId: string, threadId: string, text: string, visibility?: ThreadVisibility): Promise<void> {
  unwrap(
    await api.POST("/v1/agents/{agentId}/threads/{threadId}/messages", {
      params: { path: { agentId, threadId } },
      body: { text, requestId: newId(), ...(visibility ? { visibility } : {}) },
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

export type LiveThread = { view: ChatView | undefined; connected: boolean };

/**
 * Follows a thread's view. EventSource reconnects by itself (for example
 * while a hibernated agent wakes up); every event carries the whole view, so
 * nothing is lost across reconnects. `enabled` is false for a thread that has
 * no messages yet (the runtime answers 404 until the first one).
 */
export function useLiveThread(agentId: string, threadId: string, enabled: boolean): LiveThread {
  const [state, setState] = useState<LiveThread>({ view: undefined, connected: false });
  useEffect(() => {
    setState({ view: undefined, connected: false });
    if (!enabled) return;
    const source = new EventSource(`/v1/agents/${encodeURIComponent(agentId)}/threads/${encodeURIComponent(threadId)}/events`, {
      withCredentials: true,
    });
    source.addEventListener("view", (event) => {
      setState({ view: JSON.parse((event as MessageEvent<string>).data) as ChatView, connected: true });
    });
    source.onerror = () => setState((s) => ({ ...s, connected: false }));
    return () => source.close();
  }, [agentId, threadId, enabled]);
  return state;
}
