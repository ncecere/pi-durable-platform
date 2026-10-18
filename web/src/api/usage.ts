/* Model usage reports: requests, tokens and an estimated cost per day, model, agent or team. */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type UsageReport = Schemas["UsageReport"];
export type UsageRow = Schemas["UsageRow"];
export type TeamUsageGroup = "day" | "model" | "agent";
export type AdminUsageGroup = "day" | "team" | "model";

/** The report's days: YYYY-MM-DD, inclusive (UTC on the server); absent = the server's default, the last 30 days. */
export type UsageWindow = { from?: string; to?: string };

export const usageKey = ["usage"];

/** A team's usage (its members, its service accounts and platform staff); `agentId` narrows it to one agent. */
export const teamUsageQuery = (team: string, opts: UsageWindow & { groupBy: TeamUsageGroup; agentId?: string }) =>
  queryOptions({
    queryKey: [...usageKey, "team", team, opts],
    queryFn: async () =>
      unwrap(
        await api.GET("/v1/teams/{team}/usage", {
          params: { path: { team }, query: { from: opts.from, to: opts.to, groupBy: opts.groupBy, agentId: opts.agentId || undefined } },
        }),
      ),
  });

/** Every team's usage (platform admins and auditors). */
export const adminUsageQuery = (opts: UsageWindow & { groupBy: AdminUsageGroup }) =>
  queryOptions({
    queryKey: [...usageKey, "admin", opts],
    queryFn: async () => unwrap(await api.GET("/v1/admin/usage", { params: { query: { from: opts.from, to: opts.to, groupBy: opts.groupBy } } })),
  });
