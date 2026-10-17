/* Templates: who may do what (mirroring internal/authz/templates.go), labels and cache helpers. */
import type { QueryClient } from "@tanstack/react-query";
import {
  type AgentTemplate,
  templateQuery,
  templatesKey,
} from "../../api/templates";
import type { Me } from "../../session";

export type { AgentTemplate };

export type TemplateAbilities = {
  /** Rename, describe, archive and add versions. */
  canEdit: boolean;
  /** Ask to publish to the catalog, or take it out: the owning team's owners and admins. */
  canPublish: boolean;
  /** Approve or reject a request to publish: platform admins. */
  canReview: boolean;
  /** Teams the person may deploy it to (editors and above of active teams that may use it). */
  deployTeams: { slug: string; name: string }[];
};

export function templateAbilities(me: Me, t: AgentTemplate): TemplateAbilities {
  const mine = t.teamSlug
    ? me.teams.find((m) => m.slug === t.teamSlug)
    : undefined;
  const role = mine?.status === "active" ? mine.role : undefined;
  const editors = me.teams.filter(
    (m) => m.status === "active" && m.role !== "member",
  );
  const usable = !t.archived && (!t.teamSlug || t.status === "published");
  return {
    canEdit: t.teamSlug
      ? role !== undefined && role !== "member"
      : me.capabilities.platformAdmin,
    canPublish: !!t.teamSlug && (role === "owner" || role === "admin"),
    canReview: me.capabilities.platformAdmin && t.status === "pending",
    deployTeams: t.archived
      ? []
      : editors
          .filter((m) => usable || m.slug === t.teamSlug)
          .map((m) => ({ slug: m.slug, name: m.name })),
  };
}

export const statusLabel: Record<AgentTemplate["status"], string> = {
  private: "Private",
  pending: "Pending review",
  published: "In the catalog",
};

export const statusTone: Record<
  AgentTemplate["status"],
  "neutral" | "warning" | "success"
> = {
  private: "neutral",
  pending: "warning",
  published: "success",
};

export const ownerLabel = (t: AgentTemplate) => t.teamName ?? "Platform";

export const whatIsATemplate =
  "A template defines an agent: its instructions now, and later its tools and models. Every change is a new version; agents keep their version until they're upgraded.";

export const slugProblem = (slug: string) =>
  /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/.test(slug)
    ? undefined
    : "Lower-case letters, digits and dashes (1-64), not starting or ending with a dash.";

/** After a template changed: its record and every list. */
export function storeTemplate(qc: QueryClient, t: AgentTemplate) {
  qc.setQueryData(templateQuery(t.id).queryKey, t);
  void qc.invalidateQueries({
    queryKey: templatesKey,
    predicate: (q) => q.queryKey[1] !== t.id,
  });
}
