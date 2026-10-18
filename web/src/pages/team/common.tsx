/*
 * Shared pieces for the team area: the team context provided by the team
 * layout, and permission helpers mirroring the server's rules.
 */
import { createContext, useContext } from "react";
import type { Schemas } from "../../api/client";
import type { TeamRole } from "../../components/roles";

export type Team = Schemas["Team"];

export type TeamCtx = {
  /** Team slug, used in API paths and links. */
  slug: string;
  team: Team;
  /** The viewer's role; absent for platform admins/auditors viewing the team. */
  role?: TeamRole;
  archived: boolean;
  /** Editors and above on an active team. */
  canEdit: boolean;
  /** Admins and owners. */
  isManager: boolean;
  /** Someone's personal workspace: only its owner, no service accounts or own templates. */
  personal: boolean;
};

export function teamCtx(team: Team, role?: TeamRole): TeamCtx {
  const archived = team.status === "archived";
  const isManager = role === "owner" || role === "admin";
  return {
    slug: team.slug,
    team,
    role,
    archived,
    isManager,
    canEdit: !archived && (isManager || role === "editor"),
    personal: team.kind === "personal",
  };
}

/** Who reads the team's audit log: its editors, admins and owners (archived or not), and platform staff. */
export const canReadAudit = (role: TeamRole | undefined, staff: boolean) => role === "owner" || role === "admin" || role === "editor" || (!role && staff);

export const TeamContext = createContext<TeamCtx | null>(null);

/** The current team; only call below the team layout. */
export function useTeam(): TeamCtx {
  const ctx = useContext(TeamContext);
  if (!ctx) throw new Error("useTeam called outside the team layout");
  return ctx;
}
