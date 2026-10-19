/* Labels, badges and helpers shared by the admin users and teams pages (and the profile page). */
import type { Schemas } from "@/api/client";
import { StatusBadge } from "@/components/ui/badge/badge";

export const platformRoleLabels: Record<Schemas["PlatformRole"], string> = {
  none: "None",
  platform_admin: "Platform admin",
  platform_auditor: "Platform auditor",
};

/** Why the identity provider's group rule ended a person's access (User.accessDeniedReason). */
export const accessDeniedReasons: Record<NonNullable<Schemas["User"]["accessDeniedReason"]>, string> = {
  not_in_group: "They are no longer in a group allowed to sign in.",
  idp_refused: "The identity provider no longer vouches for them (disabled, or their grant was revoked or expired).",
  idp_unreachable: "The identity provider could not confirm their groups within the grace period.",
};

/** Active, Suspended (an admin's decision), or No longer in an allowed group (the identity provider's group rule). */
export function UserStatusBadge({ user }: { user: Pick<Schemas["User"], "status" | "accessDeniedAt" | "accessDeniedReason"> }) {
  if (user.status !== "active") return <StatusBadge tone="danger">Suspended</StatusBadge>;
  if (user.accessDeniedAt) {
    const why = user.accessDeniedReason ? accessDeniedReasons[user.accessDeniedReason] : undefined;
    return (
      <StatusBadge tone="warning" title={why}>
        Group access ended
      </StatusBadge>
    );
  }
  return <StatusBadge tone="success">Active</StatusBadge>;
}

export function TeamStatusBadge({ status }: { status: Schemas["TeamStatus"] }) {
  return status === "active" ? <StatusBadge tone="success">Active</StatusBadge> : <StatusBadge tone="warning">Archived</StatusBadge>;
}

/** A team slug suggested from its name: lowercase, hyphen-separated, at most 63 characters. */
export function slugify(name: string) {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

/** "1 owner", or "Owner invited" when none has signed in yet but one is invited, as the teams list says. */
export function ownersHint(s: Pick<Schemas["TeamSummary"], "ownerCount" | "ownerInvites">) {
  if (s.ownerCount === 0 && s.ownerInvites > 0) return s.ownerInvites === 1 ? "Owner invited" : `${s.ownerInvites} owners invited`;
  return `${s.ownerCount} ${s.ownerCount === 1 ? "owner" : "owners"}`;
}
