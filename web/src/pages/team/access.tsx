/*
 * What a team role may do, said the same way on every team page: a read-only
 * notice is one untitled info alert, "Members can view the team's members.
 * Admins and owners can add, change and remove them."
 */
import { Alert } from "@/components/ui/alert/alert";
import { roleLabels } from "../../components/roles";
import { useTeam } from "./common";

/** "Members can view the team's members. Admins and owners can change them." */
export function readOnlyText(role: keyof typeof roleLabels, what: string, who = "Editors, admins and owners", change = "change them") {
  return `${roleLabels[role]}s can view ${what}. ${who} can ${change}.`;
}

/**
 * The read-only notice for a team member who can't change `what` (nothing for those who can, an archived
 * team, which has its own notice, or platform staff without a role).
 */
export function ReadOnlyNotice({ what, managers = false, change }: { what: string; managers?: boolean; change?: string }) {
  const { role, canEdit, isManager, archived } = useTeam();
  if (!role || archived || (managers ? isManager : canEdit)) return null;
  return <Alert tone="info">{readOnlyText(role, what, managers ? "Admins and owners" : undefined, change)}</Alert>;
}
