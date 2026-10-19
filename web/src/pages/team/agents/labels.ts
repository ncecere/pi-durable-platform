/* Agents: who may do what (mirroring internal/authz/agents.go), status labels and cache helpers. */
import type { QueryClient } from "@tanstack/react-query";
import { type Agent, agentQuery, agentsKey } from "../../../api/agents";
import type { TeamCtx } from "../common";

export type { Agent };

export type AgentAbilities = {
  /** Talk to it and read its conversations (Test): the team's members, not for service-managed agents. */
  canUse: boolean;
  /** Change its settings: editors and above of an active team. */
  canEdit: boolean;
  /** Delete it: the team's owners and admins. */
  canDelete: boolean;
};

export function agentAbilities(ctx: TeamCtx, agent: Agent): AgentAbilities {
  const member = ctx.role !== undefined;
  const managed = agent.externalKey != null;
  return {
    canUse: member && !managed,
    canEdit: member && !managed && ctx.canEdit,
    canDelete: ctx.isManager,
  };
}

/** Awake, Hibernating or Off. */
export function agentStatus(agent: Agent): {
  label: string;
  tone: "success" | "neutral" | "warning";
} {
  if (!agent.enabled) return { label: "Off", tone: "warning" };
  return agent.running
    ? { label: "Awake", tone: "success" }
    : { label: "Hibernating", tone: "neutral" };
}

export const whatIsAnAgent =
  "A durable Pi agent deployed from a template. It runs in its own runtime when used, hibernates when idle, and keeps its conversations in its own storage.";

export const whatIsServiceManaged =
  "Created by one of the team's service accounts for a user of its app (with its own key). Only the team's service accounts talk to it: its conversations aren't shown here.";

/** After an agent changed: the record's cache and every list. */
export function storeAgent(qc: QueryClient, agent: Agent) {
  qc.setQueryData(agentQuery(agent.id).queryKey, agent);
  void qc.invalidateQueries({
    queryKey: agentsKey,
    predicate: (q) => q.queryKey[1] !== agent.id,
  });
}

/**
 * The agent is awake: a message was accepted, or one of its threads is replying (its runtime is up either way).
 * Hibernating is the other way round: `storeAgent` with `running: false`, after the platform says so.
 */
export function markAwake(qc: QueryClient, agentId: string) {
  qc.setQueryData(agentQuery(agentId).queryKey, (a: Agent | undefined) => (a && !a.running ? { ...a, running: true } : a));
}

export const nameProblem = (name: string) => {
  const n = name.trim();
  if (!n) return "Enter a name.";
  if (n.length > 100) return "At most 100 characters.";
  return undefined;
};
