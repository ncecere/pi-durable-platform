/* Personal workspaces: the defaults for every one (admin), and a workspace's limits and how much of them is used. */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type PersonalSettings = Schemas["PersonalWorkspaceSettings"];
export type WorkspaceLimits = Schemas["WorkspaceLimits"];
export type TeamKind = "team" | "personal";

/** A team or membership that is someone's personal workspace. */
export const isPersonal = (t: { kind?: TeamKind } | undefined | null) => t?.kind === "personal";

/** The defaults for every personal workspace (platform admins and auditors). */
export const personalSettingsQuery = () =>
  queryOptions({
    queryKey: ["admin", "settings", "personal-workspaces"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/settings/personal-workspaces")),
  });

/** A workspace's limits and use (its members and platform staff). */
export const workspaceLimitsQuery = (team: string) =>
  queryOptions({
    queryKey: ["team", team, "limits"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/limits", { params: { path: { team } } })),
  });

/** "$12.50". */
export const usd = (n: number) => `$${n.toFixed(2)}`;

/** "2 of 5 agents", "1 of 2 running at once"; without a limit, the count alone. */
export function limitLines(l: WorkspaceLimits): { agents: string; running: string; budget?: string } {
  const agents = l.maxAgents == null ? `${l.agentCount} ${l.agentCount === 1 ? "agent" : "agents"}` : `${l.agentCount} of ${l.maxAgents} agents`;
  const running = l.maxRunning == null ? `${l.runningCount} running` : `${l.runningCount} of ${l.maxRunning} running at once`;
  return { agents, running, budget: l.monthlyBudgetUsd == null ? undefined : `Monthly budget ${usd(l.monthlyBudgetUsd)} (not enforced yet)` };
}
