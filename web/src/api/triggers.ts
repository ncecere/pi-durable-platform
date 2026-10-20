/*
 * Triggers (schedules and webhooks that start runs of an agent without a person), the agent's limits for them, and
 * their runs. Reads are everyone's who may use the agent; changes are its team's owners' and admins'.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, ifMatch, type Schemas, unwrap } from "./client";

export type Trigger = Schemas["Trigger"];
export type TriggerSaved = Schemas["TriggerSaved"];
export type TriggerCreate = Schemas["TriggerCreate"];
export type TriggerUpdate = Schemas["TriggerUpdate"];
export type TriggerRun = Schemas["TriggerRun"];
export type TriggerRunStatus = Schemas["TriggerRunStatus"];
export type TriggerSettings = Schemas["TriggerSettings"];

export const triggersKey = (agentId: string) => ["agents", agentId, "triggers"];
export const runsKey = (agentId: string) => ["agents", agentId, "runs"];

export const triggersQuery = (agentId: string) =>
  queryOptions({
    queryKey: [...triggersKey(agentId), "list"],
    queryFn: async () => unwrap(await api.GET("/v1/agents/{agentId}/triggers", { params: { path: { agentId } } })),
  });

export const triggerSettingsQuery = (agentId: string) =>
  queryOptions({
    queryKey: [...triggersKey(agentId), "settings"],
    queryFn: async () => unwrap(await api.GET("/v1/agents/{agentId}/trigger-settings", { params: { path: { agentId } } })),
  });

export const RUN_PAGE = 50;

/** The agent's latest runs (one page, newest first), optionally one trigger's or one status's. */
export const runsQuery = (agentId: string, filter: { triggerId?: string; status?: TriggerRunStatus } = {}) =>
  queryOptions({
    queryKey: [...runsKey(agentId), filter],
    queryFn: async () =>
      unwrap(await api.GET("/v1/agents/{agentId}/runs", { params: { path: { agentId }, query: { limit: RUN_PAGE, ...filter } } })),
  });

export async function createTrigger(agentId: string, body: TriggerCreate): Promise<TriggerSaved> {
  return unwrap(await api.POST("/v1/agents/{agentId}/triggers", { params: { path: { agentId } }, body }));
}

export async function updateTrigger(trigger: Trigger, body: TriggerUpdate): Promise<TriggerSaved> {
  return unwrap(
    await api.PATCH("/v1/agents/{agentId}/triggers/{triggerId}", {
      params: { path: { agentId: trigger.agentId, triggerId: trigger.id }, header: ifMatch(trigger.revision) },
      body,
    }),
  );
}

export async function deleteTrigger(trigger: Trigger): Promise<void> {
  unwrap(await api.DELETE("/v1/agents/{agentId}/triggers/{triggerId}", { params: { path: { agentId: trigger.agentId, triggerId: trigger.id } } }));
}

export async function fireTrigger(trigger: Trigger): Promise<TriggerRun> {
  return unwrap(await api.POST("/v1/agents/{agentId}/triggers/{triggerId}/fire", { params: { path: { agentId: trigger.agentId, triggerId: trigger.id } } }));
}

export async function rotateTriggerSecret(trigger: Trigger, which: "webhook" | "callback"): Promise<TriggerSaved> {
  return unwrap(
    await api.POST("/v1/agents/{agentId}/triggers/{triggerId}/secret", {
      params: { path: { agentId: trigger.agentId, triggerId: trigger.id } },
      body: { which },
    }),
  );
}

export async function setTriggerSettings(agentId: string, current: TriggerSettings, paused: boolean): Promise<TriggerSettings> {
  return unwrap(
    await api.PUT("/v1/agents/{agentId}/trigger-settings", {
      params: { path: { agentId }, header: ifMatch(current.revision) },
      body: {
        paused,
        maxConcurrentRuns: current.maxConcurrentRuns ?? null,
        maxRunSeconds: current.maxRunSeconds ?? null,
        dailyBudgetUsd: current.dailyBudgetUsd ?? null,
      },
    }),
  );
}

/** Run statuses in words, with a badge tone. */
export const runStatus: Record<TriggerRunStatus, { label: string; tone: "neutral" | "info" | "success" | "warning" | "danger" }> = {
  pending: { label: "Starting", tone: "info" },
  running: { label: "Running", tone: "info" },
  succeeded: { label: "Succeeded", tone: "success" },
  failed: { label: "Failed", tone: "danger" },
  stopped: { label: "Stopped", tone: "warning" },
  skipped: { label: "Skipped", tone: "neutral" },
};

/** Why a run was skipped, stopped or failed, in words (unknown codes are spelled out). */
const reasons: Record<string, string> = {
  trigger_paused: "the trigger is paused",
  agent_paused: "the agent's triggers are paused",
  team_archived: "the team is archived",
  concurrency: "too many runs at once",
  budget: "over the daily budget",
  backoff: "backing off after a failure",
  agent_daily_runs: "the agent's own schedules ran their most for the day",
  missed: "missed while the platform was down",
  max_run_time: "ran past its maximum time",
  reply_error: "the reply failed",
  thread_deleted: "its conversation was deleted",
  delivery_failed: "the agent couldn't be reached",
  agent_moved: "the agent moved to another team before it ran",
  agent_disabled: "the agent is turned off",
  agent_unavailable: "the agent couldn't be started",
  thread_private: "its conversation is someone's private one",
  thread_reserved: "its conversation is someone's",
  thread_mcp_servers: "an app gave its conversation MCP servers",
  message_refused: "the agent refused the message",
};

export const runReason = (code: string | null | undefined) => (code ? (reasons[code] ?? code.replace(/_/g, " ")) : undefined);

export const firedByLabel: Record<TriggerRun["firedBy"], string> = { schedule: "Schedule", webhook: "Webhook", manual: "Run now" };
