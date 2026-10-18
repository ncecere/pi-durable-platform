/* Who changes a team's own MCP servers, and what the others are told (the server's rules: owners and admins of an active team). */
import type { TeamCtx } from "../common";

/** The team's owners and admins, while it's active. */
export const managesMcpServers = (ctx: Pick<TeamCtx, "isManager" | "archived">) => ctx.isManager && !ctx.archived;

/** For viewers who can't change the servers. */
export function readOnlyNote(ctx: Pick<TeamCtx, "role" | "archived" | "personal">): string {
  if (ctx.archived) return "This team is archived: its MCP servers can be viewed but not changed.";
  if (!ctx.role) return "You can view the team's MCP servers. Platform admins change them in Admin › MCP servers.";
  return `You can view the ${ctx.personal ? "workspace's" : "team's"} MCP servers. Only its owners and admins can change them.`;
}

/**
 * Why the team can't add a server now (limits.canAddMcpServers is false), or
 * undefined for those who never could (they get readOnlyNote).
 */
export function cannotAddReason(ctx: Pick<TeamCtx, "personal">): string {
  return ctx.personal
    ? "Platform admins haven't let personal workspaces add their own MCP servers. Your workspace can use the servers the platform publishes to it."
    : "Platform admins have turned off team MCP servers. The team's existing servers keep working and can still be changed or deleted.";
}
