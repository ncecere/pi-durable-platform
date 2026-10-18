/* Who changes a team's own model providers, and what the others are told (the server's rules: owners and admins of an active team). */
import type { TeamCtx } from "../common";

/** The team's owners and admins, while it's active. */
export const managesProviders = (ctx: Pick<TeamCtx, "isManager" | "archived">) => ctx.isManager && !ctx.archived;

/** For viewers who can't change the providers. */
export function readOnlyNote(ctx: Pick<TeamCtx, "role" | "archived" | "personal">): string {
  if (ctx.archived) return "This team is archived: its providers can be viewed but not changed.";
  if (!ctx.role) return "You can view the team's providers. Platform admins change them in Admin › Models.";
  return `You can view the ${ctx.personal ? "workspace's" : "team's"} providers. Only its owners and admins can change them.`;
}

/**
 * Why the team can't add a provider now (limits.canAddProviders is false), or
 * undefined for those who never could (they get readOnlyNote).
 */
export function cannotAddReason(ctx: Pick<TeamCtx, "personal">): string {
  return ctx.personal
    ? "Platform admins haven't let personal workspaces add their own model providers. Your workspace can use the models the platform publishes to it."
    : "Platform admins have turned off team model providers. The team's existing providers keep working and can still be changed or deleted.";
}
