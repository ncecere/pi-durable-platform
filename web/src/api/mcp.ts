/*
 * The MCP server catalog, in two scopes like the model catalog (models.ts).
 * Admin: the platform's servers and every team's (/v1/admin/mcp-servers…).
 * Team: a team's own servers (/v1/teams/{team}/mcp-servers/own…), proxy-only
 * and for that team alone; /v1/teams/{team}/mcp-servers lists every server the
 * team may use (its own first, then the platform's published to it). Query keys:
 *
 *   ["mcp", "admin", …]          platform admins' lists and records
 *   ["mcp", "team", team, …]     a team's own servers, and the servers it may use
 */
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, ifMatch, type Schemas, unwrap } from "./client";
import { adminScope, type ModelScope, type OwnerFilter } from "./models";

export type McpServer = Schemas["McpServer"];
export type McpTool = Schemas["McpTool"];
/** A server a template version attaches (in a create body): all its tools, or `tools`. */
export type McpAttach = Schemas["McpAttach"];
/** A server a template version has, with its name and slug. */
export type McpAttached = Schemas["McpAttached"];

/** Whose servers: the platform admin's routes, or one team's own (the same scopes as models). */
export type McpScope = ModelScope;

export const mcpKey = ["mcp"];
const scopeKey = (scope: McpScope) => (scope.kind === "admin" ? [...mcpKey, "admin"] : [...mcpKey, "team", scope.team]);

/** Admin: every owner's servers (or one owner's: "platform", a team's slug); team: the team's own. */
export const mcpServersQuery = (scope: McpScope = adminScope, owner: OwnerFilter = "all") =>
  queryOptions({
    queryKey: [...scopeKey(scope), "servers", "list", scope.kind === "admin" ? owner : "own"],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/mcp-servers", { params: { query: owner === "all" ? {} : { owner } } }))
        : unwrap(await api.GET("/v1/teams/{team}/mcp-servers/own", { params: { path: { team: scope.team } } })),
  });

export const mcpServerQuery = (scope: McpScope, serverId: string) =>
  queryOptions({
    queryKey: [...scopeKey(scope), "servers", serverId],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId } } }))
        : unwrap(await api.GET("/v1/teams/{team}/mcp-servers/own/{serverId}", { params: { path: { team: scope.team, serverId } } })),
  });

/** The servers a team may use: its own (with `team`) and the platform's published to it, enabled, with their tools. */
export const teamMcpServersQuery = (team: string) =>
  queryOptions({
    queryKey: [...mcpKey, "team", team, "usable"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/mcp-servers", { params: { path: { team } } })),
  });

/*
 * After a server changed. A team's change shows in its own lists and the
 * admin's; an admin's change may reach any team, so everything.
 */
export async function invalidateMcp(qc: QueryClient, scope: McpScope = adminScope): Promise<void> {
  if (scope.kind === "admin") return qc.invalidateQueries({ queryKey: mcpKey });
  await Promise.all([qc.invalidateQueries({ queryKey: scopeKey(scope) }), qc.invalidateQueries({ queryKey: scopeKey(adminScope) })]);
}

/* ---------------- changes ---------------- */

export async function createMcpServer(scope: McpScope, body: Schemas["McpServerCreate"]) {
  if (scope.kind === "admin") return unwrap(await api.POST("/v1/admin/mcp-servers", { body }));
  return unwrap(await api.POST("/v1/teams/{team}/mcp-servers/own", { params: { path: { team: scope.team } }, body }));
}

export async function updateMcpServer(scope: McpScope, m: Pick<McpServer, "id" | "revision">, body: Schemas["McpServerUpdate"]) {
  const header = ifMatch(m.revision);
  if (scope.kind === "admin") return unwrap(await api.PATCH("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId: m.id }, header }, body }));
  return unwrap(await api.PATCH("/v1/teams/{team}/mcp-servers/own/{serverId}", { params: { path: { team: scope.team, serverId: m.id }, header }, body }));
}

export async function deleteMcpServer(scope: McpScope, serverId: string) {
  if (scope.kind === "admin") return unwrap(await api.DELETE("/v1/admin/mcp-servers/{serverId}", { params: { path: { serverId } } }));
  return unwrap(await api.DELETE("/v1/teams/{team}/mcp-servers/own/{serverId}", { params: { path: { team: scope.team, serverId } } }));
}

/** Reads the server's tools again and stores the result. */
export async function refreshMcpServer(scope: McpScope, serverId: string) {
  if (scope.kind === "admin") return unwrap(await api.POST("/v1/admin/mcp-servers/{serverId}/refresh", { params: { path: { serverId } } }));
  return unwrap(await api.POST("/v1/teams/{team}/mcp-servers/own/{serverId}/refresh", { params: { path: { team: scope.team, serverId } } }));
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
