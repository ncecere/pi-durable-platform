/*
 * Where an MCP server page works: Admin › MCP servers (the platform's servers
 * and every team's) or Team › MCP servers (the team's own). A server's page,
 * its tabs and the add dialog take an McpCatalogScope and link, navigate and
 * call the API through it, so both areas share them (as models/scope.tsx).
 *
 * A team's own server (in the team's pages, or a team-owned one in Admin) is
 * proxy-only and for its team alone: no access mode to choose and no
 * publishing (visibility, teams, personal workspaces).
 */
import { Link, useNavigate } from "@tanstack/react-router";
import type { ReactElement } from "react";
import type { McpScope, McpServer } from "@/api/mcp";

export type McpCatalogScope = {
  scope: McpScope;
  /** May add, change and delete (platform admins in Admin; the team's owners and admins in the team). */
  canManage: boolean;
  /** Said to viewers who can't change it. */
  readOnlyNote: string;
};

/** A link to one of the scope's servers (rendered by TextLink and row menus). */
export type ServerLink = (serverId: string, search?: Record<string, string>) => ReactElement;

export function serverLinkFor(scope: McpScope): ServerLink {
  if (scope.kind === "admin") {
    return (serverId, search = {}) => <Link to="/admin/mcp-servers/$serverId" params={{ serverId }} search={search} />;
  }
  const team = scope.team;
  return (serverId, search = {}) => <Link to="/teams/$team/mcp-servers/$serverId" params={{ team, serverId }} search={search} />;
}

/** Opens a server's page, or goes back to the scope's list. */
export function useMcpNavigate(scope: McpScope) {
  const navigate = useNavigate();
  return {
    server: (serverId: string) =>
      scope.kind === "admin"
        ? navigate({ to: "/admin/mcp-servers/$serverId", params: { serverId } })
        : navigate({ to: "/teams/$team/mcp-servers/$serverId", params: { team: scope.team, serverId } }),
    list: () =>
      scope.kind === "admin"
        ? navigate({ to: "/admin/mcp-servers", replace: true })
        : navigate({ to: "/teams/$team/mcp-servers", params: { team: scope.team }, replace: true }),
  };
}

/** A team's own server: proxy only, and its team's alone. */
export const isTeamOwned = (scope: McpScope, server: Pick<McpServer, "team">) => scope.kind === "team" || Boolean(server.team);
