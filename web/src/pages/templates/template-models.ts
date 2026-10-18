/*
 * The models and MCP servers a template may use: its team's (the platform's
 * published to it and its own providers', enabled), or for a platform template
 * every enabled platform model. Teams' own models never go where every team
 * may deploy: platform templates, and a team template in the catalog or
 * waiting for review (`catalog`); the API refuses them (409 team_model).
 */
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "../../api/client";
import { type McpServer, mcpServersQuery, teamMcpServersQuery } from "../../api/mcp";
import { adminModelsQuery, type Model, teamModelsQuery } from "../../api/models";

export function useTemplateModels(team: string | null | undefined, { enabled = true, catalog = false } = {}): { models: Model[] | undefined; loading: boolean } {
  const teamModels = useQuery({ ...teamModelsQuery(team ?? ""), enabled: enabled && !!team });
  const platform = useQuery({ ...adminModelsQuery("platform"), enabled: enabled && !team });
  if (team) return { models: catalog ? teamModels.data?.filter((m) => !m.providerTeam) : teamModels.data, loading: teamModels.isLoading };
  return { models: platform.data?.filter((m) => !m.providerTeam && m.enabled && m.providerEnabled !== false), loading: platform.isLoading };
}

/** Said where a template in the catalog may only run on platform models. */
export const catalogModelsNote = "Templates in the catalog run on platform models only: the team's own models aren't offered.";

/** 409 team_model: the template (or its version) runs on a team's own model where only platform models may go. */
export function teamModelMessage(err: unknown, what: "submit" | "approve" | "version"): string | undefined {
  if (!(err instanceof ApiError) || err.code !== "team_model") return undefined;
  if (what === "version") return "A template in the catalog, or waiting for review, can't run on the team's own models: only the team can use them. Choose a platform model.";
  return `Its current version runs on one of the team's own models, which only the team can use. ${
    what === "submit" ? "Save a new version on a platform model, then submit it again." : "Ask the team to save a version on a platform model first."
  }`;
}

export function useTemplateMcpServers(team: string | null | undefined): { servers: McpServer[] | undefined; loading: boolean; emptyText: string } {
  const teamServers = useQuery({ ...teamMcpServersQuery(team ?? ""), enabled: !!team });
  const all = useQuery({ ...mcpServersQuery(), enabled: !team });
  if (team) return { servers: teamServers.data, loading: teamServers.isLoading, emptyText: "No MCP servers are published to the team yet." };
  return { servers: all.data?.filter((m) => m.enabled), loading: all.isLoading, emptyText: "No MCP servers are enabled yet." };
}
