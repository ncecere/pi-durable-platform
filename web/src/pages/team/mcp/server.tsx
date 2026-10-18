/*
 * Team › MCP servers › one of the team's own servers
 * (/teams/$team/mcp-servers/$serverId): Admin's server page in the team's
 * scope. Proxy only and for the team alone (no access mode or publishing);
 * the team's owners and admins change it, its other members read.
 */
import { useParams } from "@tanstack/react-router";
import { teamScope } from "@/api/models";
import { McpServerPage } from "../../admin/mcp/server";
import { useTeam } from "../common";
import { managesMcpServers, readOnlyNote } from "./access";

export function TeamMcpServerPage() {
  const { serverId } = useParams({ from: "/app/teams/$team/mcp-servers/$serverId" });
  const ctx = useTeam();
  return <McpServerPage serverId={serverId} catalog={{ scope: teamScope(ctx.slug), canManage: managesMcpServers(ctx), readOnlyNote: readOnlyNote(ctx) }} />;
}
