/*
 * Agents: queries, and a live view of one conversation over server-sent
 * events. The platform relays thread routes to the agent's runtime, waking it
 * first; the runtime is the source of truth for the transcript.
 */
import { queryOptions } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api, type Schemas, unwrap } from "./client";

export type Agent = Schemas["Agent"];
export type ChatView = Schemas["ChatView"];
export type ChatMessage = Schemas["ChatMessage"];
export type AgentThread = Schemas["AgentThread"];

export const agentsKey = ["agents"];

/** The agents the person may use: their assistant, then their teams' agents. */
export const agentsQuery = () =>
  queryOptions({ queryKey: [...agentsKey, "list"], queryFn: async () => unwrap(await api.GET("/v1/agents")) });

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

/** The person's assistant, created on first use. */
export async function ensureAssistant(): Promise<Agent> {
  return unwrap(await api.POST("/v1/me/assistant"));
}

export async function sendMessage(agentId: string, threadId: string, text: string): Promise<void> {
  unwrap(
    await api.POST("/v1/agents/{agentId}/threads/{threadId}/messages", {
      params: { path: { agentId, threadId } },
      body: { text, requestId: newId() },
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
