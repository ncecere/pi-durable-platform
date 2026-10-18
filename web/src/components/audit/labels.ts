/* Human-readable audit actions, action groups (areas), actors and target types. */
import type { Schemas } from "../../api/client";
import type { FacetOption } from "@/components/ui/filter-bar/filter-bar";

type AuditEntry = Schemas["AuditEntry"];

/** Where a log is shown. On a team's log, `member` is false for platform staff. */
export type AuditScope = { kind: "team"; team: string; member?: boolean } | { kind: "platform" };

/** The query key prefix of a log's queries (invalidated together). */
export const auditKey = (scope: AuditScope) => (scope.kind === "team" ? ["team", scope.team, "audit"] : ["admin", "audit"]);

const actionLabels: Record<string, string> = {
  "auth.login": "Signed in",
  "auth.logout": "Signed out",
  "auth.login_denied": "Sign-in denied",
  "platform.bootstrap_role": "Granted initial platform role",
  "platform.user_update": "Changed user",
  "platform.user_role_change": "Changed platform role",
  "platform.user_suspend": "Suspended user",
  "platform.user_reactivate": "Reactivated user",
  "team.create": "Created team",
  "team.update": "Changed team",
  "team.archive": "Archived team",
  "team.unarchive": "Unarchived team",
  "team.member_add": "Added member",
  "team.member_remove": "Removed member",
  "team.member_leave": "Left team",
  "team.member_role_change": "Changed member role",
  "team.owner_assign": "Assigned owner",
  "team.invite_create": "Invited member",
  "team.invite_revoke": "Revoked invite",
  "team.invite_accept": "Accepted invite",
  "team.personal_create": "Created personal workspace",
  "team.personal_limits": "Changed personal workspace limits",
  "platform.personal_settings": "Changed personal workspace defaults",
  "api_key.create": "Created API key",
  "api_key.revoke": "Revoked API key",
  "service_account.create": "Created service account",
  "service_account.update": "Changed service account",
  "service_account.delete": "Deleted service account",
  "service_account_key.create": "Created service account key",
  "service_account_key.revoke": "Revoked service account key",
  "api_key.pepper_rehash": "Re-secured API key after key rotation",
  "service_account_key.pepper_rehash": "Re-secured service account key after key rotation",
};

/** "Added member" for team.member_add; unknown actions are spelled out ("foo.bar_baz" → "Foo: bar baz"). */
export function actionLabel(action: string, _entry?: Partial<Pick<AuditEntry, "after" | "metadata">>) {
  const known = actionLabels[action];
  if (known) return known;
  const [group = "", rest = ""] = action.split(".");
  const words = rest.replace(/_/g, " ");
  return `${group.charAt(0).toUpperCase()}${group.slice(1)}${words ? `: ${words}` : ""}`;
}

/**
 * Action groups for the filter; `platform` groups are only offered in the platform log. Later phases add theirs.
 */
export const actionGroups: { prefix: string; label: string; platform?: boolean }[] = [
  { prefix: "team.", label: "Team and members" },
  { prefix: "service_account.", label: "Service accounts" },
  { prefix: "service_account_key.", label: "Service account keys" },
  { prefix: "api_key.", label: "API keys", platform: true },
  { prefix: "platform.", label: "Platform and users", platform: true },
  { prefix: "auth.", label: "Sign-in", platform: true },
];

/** The area an action belongs to, by its code's group. */
export function areaOf(action: string): string | undefined {
  return actionGroups.find((g) => action.startsWith(g.prefix))?.label;
}

/**
 * The audit log's Area filter: every area, then every known action under its area, so typing "invite" finds "Invited
 * member". An area's value is its prefix ("team."), an action's its code; the API takes both.
 */
export function areaOptions(platform: boolean): FacetOption[] {
  const areas = actionGroups.filter((g) => platform || !g.platform);
  const shown = new Set(areas.map((g) => g.label));
  const actions = Object.entries(actionLabels)
    .map(([value, label]) => ({ value, label, group: areaOf(value) }))
    .filter((o): o is FacetOption & { group: string } => Boolean(o.group && shown.has(o.group)))
    .sort((a, b) => a.group.localeCompare(b.group) || a.label.localeCompare(b.label));
  return [...areas.map((g) => ({ value: g.prefix, label: `${g.label} (all)`, group: "Areas" })), ...actions];
}

/** The Person filter's entry for changes the system made (the API's actorKind=system). */
export const systemPerson = "system";
export const systemPersonOption: FacetOption = { value: systemPerson, label: "System" };

/** The Person filter's value as API filters: a person's ID, or the system as the actor. */
export function personFilter(value: string | undefined): { actorUserId?: string; actorKind?: "system" } {
  if (!value) return {};
  return value === systemPerson ? { actorKind: "system" } : { actorUserId: value };
}

/** Who did it: a person's name, or "System". */
export function actorName(actor: AuditEntry["actor"], _entry?: Pick<AuditEntry, "metadata" | "teamName">) {
  if (actor.kind === "system") return "System";
  return actor.displayName || actor.email || (actor.userId ? "Unknown user" : "Unknown");
}

/** How the person acted, in words ("Signed in", "API key: Nightly sync"); "" for the system. */
export function viaLabel(via: AuditEntry["via"]) {
  if (!via) return "";
  const named = (what: string) => (via.name ? `${what}: ${via.name}` : what);
  return via.kind === "api_key" ? named("API key") : "Signed in";
}

type ActedFields = Pick<AuditEntry, "actor" | "metadata"> & Partial<Pick<AuditEntry, "via">>;

/** How the person acted, when it wasn't in the app: "API key: Nightly sync". */
export function actedVia(e: ActedFields): string | undefined {
  if (e.via) return e.via.kind === "api_key" ? viaLabel(e.via) : undefined;
  if (e.actor.kind === "api_key") return `API key${e.actor.apiKeyName ? `: ${e.actor.apiKeyName}` : ""}`;
  return undefined;
}

/** The same for the record page's How row: a person without a key acted in the app. */
export function actedHow(e: ActedFields): string | undefined {
  if (e.via) return viaLabel(e.via) || undefined;
  return actedVia(e) ?? (e.actor.kind === "user" ? "Signed in" : undefined);
}

export const targetTypeLabels: Record<string, string> = {
  team: "Team",
  team_invite: "Invite",
  user: "User",
  api_key: "API key",
  service_account: "Service account",
  service_account_key: "Service account key",
};

/** An entry's action and target in one line for a short list ("Added member: Una User"). */
export function entryTitle(e: Pick<AuditEntry, "action" | "after" | "targetLabel">) {
  const action = actionLabel(e.action, e);
  return e.targetLabel && !action.toLowerCase().includes(e.targetLabel.toLowerCase()) ? `${action}: ${e.targetLabel}` : action;
}

export const targetTypeLabel = (type: string) => targetTypeLabels[type] ?? type.replace(/_/g, " ");
