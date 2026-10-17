/* The Target cell of an audit entry: its name, linked when it still exists and the viewer can open it. */
import { Link } from "@tanstack/react-router";
import type { ReactElement } from "react";
import type { Schemas } from "../../api/client";
import s from "../../pages/shared.module.css";
import { TextLink } from "@/components/ui/text-link/text-link";
import { type AuditScope, targetTypeLabel } from "./labels";

type AuditEntry = Schemas["AuditEntry"];

export type { AuditScope };

/** A string field the entry recorded, in its after, before or metadata (a key's serviceAccountId). */
function recorded(e: AuditEntry, key: string): string | undefined {
  for (const side of [e.after, e.before, e.metadata]) {
    const v = side && typeof side === "object" && !Array.isArray(side) ? (side as Record<string, unknown>)[key] : undefined;
    if (typeof v === "string" && v) return v;
  }
  return undefined;
}

/** The service account a key belongs to. */
const keyAccount = (e: AuditEntry) => recorded(e, "serviceAccountId");

/** A service account's record, or the one a key belongs to (its Keys tab); staff read any team's. */
function serviceAccountLink(team: string, e: AuditEntry): ReactElement | null {
  if (e.targetType === "service_account") return <Link to="/teams/$team/service-accounts/$serviceAccountId" params={{ team, serviceAccountId: e.targetId }} />;
  const account = keyAccount(e);
  return account ? <Link to="/teams/$team/service-accounts/$serviceAccountId" params={{ team, serviceAccountId: account }} search={{ tab: "keys" }} /> : null;
}

function teamLink(team: string, e: AuditEntry): ReactElement | null {
  switch (e.targetType) {
    case "team":
      return <Link to="/teams/$team" params={{ team }} />;
    case "team_invite":
      // Open invites are listed under Team settings › Members.
      return <Link to="/teams/$team/settings" params={{ team }} />;
    case "service_account":
    case "service_account_key":
      return serviceAccountLink(team, e);
    default:
      return null;
  }
}

function platformLink(e: AuditEntry): ReactElement | null {
  const id = e.targetId;
  switch (e.targetType) {
    case "team":
      // By its slug, like every other link to it.
      return <Link to="/admin/teams/$team" params={{ team: e.teamId === id && e.teamSlug ? e.teamSlug : id }} />;
    case "team_invite": {
      const team = e.teamSlug ?? e.teamId;
      return team ? <Link to="/admin/teams/$team" params={{ team }} search={{ tab: "members" }} /> : null;
    }
    case "user":
      return <Link to="/admin/users/$userId" params={{ userId: id }} />;
    case "service_account":
    case "service_account_key":
      return e.teamSlug ? serviceAccountLink(e.teamSlug, e) : null;
    default:
      return null;
  }
}

export function AuditTarget({ entry, scope }: { entry: AuditEntry; scope: AuditScope }) {
  const type = targetTypeLabel(entry.targetType);
  const name = entry.targetLabel;
  const link = entry.targetExists ? (scope.kind === "team" ? teamLink(scope.team, entry) : platformLink(entry)) : null;
  if (!name) {
    return (
      <>
        <span>{type}</span>
        {entry.targetId && <span className={`${s.secondary} ${s.mono}`}>{entry.targetId}</span>}
      </>
    );
  }
  return (
    <>
      {link ? (
        <TextLink render={link} className={s.primary}>
          {name}
        </TextLink>
      ) : (
        <span className={s.primary}>{name}</span>
      )}
      {(type !== name || !entry.targetExists) && (
        <span className={s.secondary}>
          {type}
          {!entry.targetExists && " (deleted)"}
        </span>
      )}
    </>
  );
}
