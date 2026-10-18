/*
 * The models and MCP servers a template may use: its team's (the platform's
 * published to it and its own providers', enabled), or for a platform template
 * every enabled platform model. Teams' own models never go where every team
 * may deploy: platform templates, and a team template in the catalog or
 * waiting for review (`catalog`); the API refuses them (409 team_model).
 * Likewise a team's own MCP servers (409 team_mcp_server).
 */
import { useQuery } from "@tanstack/react-query";
import { ApiError } from "../../api/client";
import { type McpServer, mcpServersQuery, teamMcpServersQuery } from "../../api/mcp";
import { adminModelsQuery, adminScope, type Model, teamModelsQuery } from "../../api/models";

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

/** Said where a template in the catalog may only attach platform MCP servers. */
export const catalogMcpNote = "Templates in the catalog get platform MCP servers only: the team's own servers aren't offered.";

/** A server the current version has that a template in the catalog isn't offered. */
export const catalogMcpUnavailable = "Not offered here: turned off, no longer published to the team, or one of the team's own servers, which templates in the catalog can't use.";

/**
 * The MCP servers a template may attach: a team template its team's own (with
 * `team`, listed first) and the platform's published to it; in the catalog
 * or waiting for review (`catalog`), and for a platform template, platform
 * servers only (the API refuses a team's: 409 team_mcp_server).
 */
export function useTemplateMcpServers(
  team: string | null | undefined,
  { catalog = false } = {},
): { servers: McpServer[] | undefined; loading: boolean; emptyText: string } {
  const teamServers = useQuery({ ...teamMcpServersQuery(team ?? ""), enabled: !!team });
  const platform = useQuery({ ...mcpServersQuery(adminScope, "platform"), enabled: !team });
  if (team) {
    return {
      servers: catalog ? teamServers.data?.filter((m) => !m.team) : teamServers.data,
      loading: teamServers.isLoading,
      emptyText: catalog ? "No platform MCP servers are published to the team yet." : "No MCP servers are published to the team, and it has none of its own yet.",
    };
  }
  return { servers: platform.data?.filter((m) => m.enabled && !m.team), loading: platform.isLoading, emptyText: "No MCP servers are enabled yet." };
}

/** 409 team_mcp_server: the template (or its version) attaches a team's own MCP server where only platform servers may go. */
export function teamMcpMessage(err: unknown, what: "submit" | "approve" | "version"): string | undefined {
  if (!(err instanceof ApiError) || err.code !== "team_mcp_server") return undefined;
  if (what === "version") return "A template in the catalog, or waiting for review, can't use the team's own MCP servers: only the team can use them. Remove them, or pick platform servers.";
  return `Its current version uses one of the team's own MCP servers, which only the team can use. ${
    what === "submit" ? "Save a new version without it, then submit it again." : "Ask the team to save a version without it first."
  }`;
}

/** Why the catalog refused a template: a team's own model (team_model) or MCP server (team_mcp_server). */
export const catalogRefusal = (err: unknown, what: "submit" | "approve" | "version") => teamModelMessage(err, what) ?? teamMcpMessage(err, what);
