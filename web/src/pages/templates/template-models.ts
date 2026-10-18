/* The models and MCP servers a template may use: its team's (published to it, enabled), or for a platform template every enabled one. */
import { useQuery } from "@tanstack/react-query";
import { type McpServer, mcpServersQuery, teamMcpServersQuery } from "../../api/mcp";
import { adminModelsQuery, type Model, teamModelsQuery } from "../../api/models";

export function useTemplateModels(team: string | null | undefined, enabled = true): { models: Model[] | undefined; loading: boolean } {
  const teamModels = useQuery({ ...teamModelsQuery(team ?? ""), enabled: enabled && !!team });
  const all = useQuery({ ...adminModelsQuery(), enabled: enabled && !team });
  if (team) return { models: teamModels.data, loading: teamModels.isLoading };
  return { models: all.data?.filter((m) => m.enabled && m.providerEnabled !== false), loading: all.isLoading };
}

export function useTemplateMcpServers(team: string | null | undefined): { servers: McpServer[] | undefined; loading: boolean; emptyText: string } {
  const teamServers = useQuery({ ...teamMcpServersQuery(team ?? ""), enabled: !!team });
  const all = useQuery({ ...mcpServersQuery(), enabled: !team });
  if (team) return { servers: teamServers.data, loading: teamServers.isLoading, emptyText: "No MCP servers are published to the team yet." };
  return { servers: all.data?.filter((m) => m.enabled), loading: all.isLoading, emptyText: "No MCP servers are enabled yet." };
}
