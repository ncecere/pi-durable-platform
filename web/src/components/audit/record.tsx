/* Parts of an audit entry's record page shared by the platform log and a team's log: what changed, and the entry's team. */
import { Link } from "@tanstack/react-router";
import type { Schemas } from "@/api/client";
import type { RecordSection } from "@/components/templates/record-page";
import { CodeBlock } from "@/components/ui/code-block/code-block";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../pages/shared.module.css";
import { auditChange } from "./changes";
import { AuditChangesTable } from "./changes-table";
import { actionLabel, auditReason } from "./labels";

type Entry = Schemas["AuditEntry"];

/** The team a team's entry belongs to, linked to its admin page ("Deleted team" when it's gone). */
export function AuditTeam({ entry }: { entry: Pick<Entry, "teamId" | "teamName" | "teamSlug"> }) {
  if (!entry.teamName || !entry.teamSlug) return <span className={s.muted}>Deleted team</span>;
  return <TextLink render={<Link to="/admin/teams/$team" params={{ team: entry.teamSlug }} />}>{entry.teamName}</TextLink>;
}

/** The before and after (in words: auditChange) and the raw metadata. */
export function auditSections(e: Entry): RecordSection[] {
  const out: RecordSection[] = [];
  const reason = auditReason(e);
  if (reason) out.push({ title: "Reason", content: <p className={s.written}>{reason}</p> });
  if (e.before != null || e.after != null) {
    const change = auditChange(e);
    out.push({
      title: "Changes",
      content: <AuditChangesTable label={`What changed: ${actionLabel(e.action, e)}`} before={change.before} after={change.after} />,
    });
  }
  if (Object.keys(e.metadata).length > 0) {
    out.push({ title: "Metadata", content: <CodeBlock code={JSON.stringify(e.metadata, null, 2)} language="json" /> });
  }
  return out;
}
