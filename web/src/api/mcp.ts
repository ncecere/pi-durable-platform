/* The MCP server catalog: servers and their tools (admin), and the servers a team may attach to its templates. */
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type McpServer = Schemas["McpServer"];
export type McpTool = Schemas["McpTool"];
/** A server a template version attaches (in a create body): all its tools, or `tools`. */
export type McpAttach = Schemas["McpAttach"];
/** A server a template version has, with its name and slug. */
export type McpAttached = Schemas["McpAttached"];

export const mcpKey = ["mcp"];

/** Every MCP server (platform admins and auditors). */
export const mcpServersQuery = () =>
  queryOptions({
    queryKey: [...mcpKey, "servers"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/mcp-servers")),
  });

export const mcpServerQuery = (serverId: string) =>
  queryOptions({
    queryKey: [...mcpKey, "servers", serverId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId } } })),
  });

/** The servers a team may attach: published to it and enabled, with their tools. */
export const teamMcpServersQuery = (team: string) =>
  queryOptions({
    queryKey: [...mcpKey, "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/mcp-servers", { params: { path: { team } } })),
  });

/** After a server changed: every server list and record. */
export function invalidateMcp(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: mcpKey });
}

/** The tool-name slug the server derives from a name ("GitHub Issues" → "github_issues"): lower case, other characters as one "_", at most 32. */
export function mcpSlugFrom(name: string): string {
  let out = "";
  for (const ch of name.toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) out += ch;
    else if (out && !out.endsWith("_")) out += "_";
  }
  out = out.replace(/^_+|_+$/g, "");
  return out.length > 32 ? out.slice(0, 32).replace(/_+$/, "") : out;
}

/** The server's rule for a slug: 1-32 lower-case letters, digits or underscores, not starting or ending with "_". */
export const mcpSlugPattern = /^[a-z0-9](?:[a-z0-9_]{0,30}[a-z0-9])?$/;

/** "Fake Tools: all tools" or "Fake Tools: echo, search". */
export const mcpSummary = (m: Pick<McpAttached, "name" | "tools">) => `${m.name}: ${m.tools == null ? "all tools" : m.tools.join(", ") || "no tools"}`;
