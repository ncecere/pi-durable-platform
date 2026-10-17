/*
 * An audit entry's before and after, in words people use: roles and statuses
 * by their labels, internal ids left out, and no value as a missing key (the
 * diff says "Not set" for it, the one word for no value). Entries without a
 * known shape are shown as recorded.
 */
import type { Schemas } from "@/api/client";
import { roleLabels } from "@/components/roles";

type AuditEntry = Schemas["AuditEntry"];
type Format = (value: unknown) => unknown;
/** A field's label and formatting; null leaves the field out. */
type Fields = Record<string, [label: string, format?: Format] | null>;

/** A switch's value in words: "On", "Off". */
export const onOff = (v: unknown) => (typeof v === "boolean" ? (v ? "On" : "Off") : v);
const label =
  (labels: Record<string, string>): Format =>
  (v) =>
    typeof v === "string" ? (labels[v] ?? v) : v;
const userStatusLabels: Record<string, string> = { active: "Active", suspended: "Suspended" };
const teamStatusLabels: Record<string, string> = { active: "Active", archived: "Archived" };
const platformRoleLabels: Record<string, string> = { none: "None", platform_admin: "Platform admin", platform_auditor: "Platform auditor" };

const serviceAccountRoleLabels: Record<string, string> = { member: roleLabels.member, editor: roleLabels.editor };

function fieldsFor(action: string): Fields | undefined {
  switch (action) {
    case "service_account.create":
    case "service_account.update":
    case "service_account.delete":
      return { role: ["Role", label(serviceAccountRoleLabels)], enabled: ["Enabled", onOff], teamId: null };
    case "service_account_key.create":
    case "service_account_key.revoke":
      return { serviceAccount: ["Service account"], expiresAt: ["Expires"], serviceAccountId: null };
    case "platform.bootstrap_role":
    case "platform.user_suspend":
    case "platform.user_reactivate":
    case "platform.user_role_change":
    case "platform.user_update":
      return { platformRole: ["Platform role", label(platformRoleLabels)], status: ["Status", label(userStatusLabels)] };
    case "team.member_add":
    case "team.member_role_change":
    case "team.invite_create":
      return { role: ["Role", label(roleLabels)] };
    case "team.create":
    case "team.update":
    case "team.archive":
    case "team.unarchive":
      return { status: ["Status", label(teamStatusLabels)] };
    default:
      return undefined;
  }
}

const acronyms: Record<string, string> = { id: "ID", ids: "IDs", url: "URL", urls: "URLs", api: "API", sso: "SSO", oidc: "OIDC", mcp: "MCP" };

/** A recorded key in plain words: "ownerEmail" → "Owner email", "userIds" → "User IDs", "warn_percent" → "Warn percent". */
export function plainKey(key: string): string {
  const words = key
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .split(/[\s_]+/)
    .filter(Boolean)
    .map((w) => acronyms[w.toLowerCase()] ?? w.toLowerCase());
  const [first = "", ...rest] = words;
  return [first.charAt(0).toUpperCase() + first.slice(1), ...rest].join(" ");
}

/** One side of a change, with known fields relabelled and formatted, and empty values left out. */
function readable(side: unknown, fields: Fields): Record<string, unknown> {
  if (!side || typeof side !== "object" || Array.isArray(side)) return {};
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(side)) {
    if (value === null || value === undefined || value === "") continue;
    const field = fields[key];
    if (field === null) continue;
    out[field?.[0] ?? plainKey(key)] = field?.[1] ? field[1](value) : value;
  }
  return out;
}

/** The entry's before and after for the diff. */
export function auditChange(e: Pick<AuditEntry, "action" | "before" | "after">): { before: object; after: object } {
  const fields = fieldsFor(e.action) ?? {};
  const side = (v: unknown) => (v && typeof v === "object" && !Array.isArray(v) ? readable(v, fields) : (v ?? {}));
  return { before: side(e.before) as object, after: side(e.after) as object };
}

/** One changed field of an entry: its plain name, and its value before and after (undefined: not set). */
export type ChangeRow = { field: string; before?: unknown; after?: unknown };

/** The fields that differ between the two sides, in order (the before's fields first). */
export function changedRows(change: { before: object; after: object }): ChangeRow[] {
  const b = change.before as Record<string, unknown>;
  const a = change.after as Record<string, unknown>;
  const keys = [...Object.keys(b), ...Object.keys(a).filter((k) => !(k in b))];
  return keys.filter((k) => JSON.stringify(b[k]) !== JSON.stringify(a[k])).map((k) => ({ field: k, before: b[k], after: a[k] }));
}

/** A value for people: text as is, true and false as On and Off, a list of words joined, anything else as compact JSON; "Not set" without one. */
export function valueText(v: unknown, none = "Not set"): string {
  if (v === undefined || v === null || v === "") return none;
  if (typeof v === "string") return v;
  if (typeof v === "boolean") return v ? "On" : "Off";
  if (typeof v === "number") return String(v);
  if (Array.isArray(v) && v.every((x) => typeof x === "string" || typeof x === "number")) return v.length ? v.join(", ") : none;
  return JSON.stringify(v);
}

const simple = (v: unknown) => v === undefined || ((typeof v === "string" || typeof v === "number" || typeof v === "boolean") && String(v).length <= 40);

/**
 * A change in one line for the list, when it's simple: one or two fields with short values, such as "Role Member →
 * Editor". Null for creations, deletions and larger changes (the record page has them).
 */
export function changeSummary(e: Pick<AuditEntry, "action" | "before" | "after">): string | null {
  if (e.before == null || e.after == null) return null;
  const rows = changedRows(auditChange(e));
  if (rows.length === 0 || rows.length > 2 || !rows.every((r) => simple(r.before) && simple(r.after))) return null;
  // "Not set", as the record page's diff says it.
  return rows.map((r) => `${r.field} ${valueText(r.before)} → ${valueText(r.after)}`).join("; ");
}
