/*
 * Admin → Teams › one team: a DetailPage with Overview · Members · Settings.
 * A personal workspace has no members to manage or owner to assign: it shows
 * its owner and Overview · Limits · Settings instead. Archive is a menu
 * action and a Danger zone entry, never a solid red header button.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";
import { Archive, ArchiveRestore, ExternalLink, Gauge, LayoutDashboard, Settings2, UsersRound } from "lucide-react";
import { useState } from "react";
import { api, ifMatch, unwrap, type Schemas } from "@/api/client";
import { isPersonal } from "@/api/personal";
import { adminTeamQuery } from "@/api/queries";
import { isNotFound, NotFoundState } from "@/components/not-found";
import { PageSkeleton } from "@/components/page-skeleton";
import { DetailPage } from "@/components/templates/detail-page";
import { Alert, ErrorAlert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { AlertDialog } from "@/components/ui/dialog/dialog";
import { toast } from "@/components/ui/toast/toast";
import { plural } from "@/lib/format";
import { adminTeamTabs } from "@/lib/tabs";
import { useIsPlatformAdmin } from "../hooks";
import { TeamStatusBadge } from "./common";
import { usePersonalOwner } from "./personal-overview";
import { TeamLimitsTab } from "./team-limits";
import { TeamMembersTab } from "./team-members";
import { TeamOverviewTab } from "./team-overview";
import { TeamSettingsTab } from "./team-settings";

/** PATCH the team (details or status) against the loaded revision. */
export function useTeamUpdate(team: string, revision: number | undefined, onDone?: () => void) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: Schemas["TeamUpdate"]) => unwrap(await api.PATCH("/v1/admin/teams/{team}", { params: { path: { team }, header: ifMatch(revision!) }, body })),
    onSuccess: (_, body) => {
      onDone?.();
      toast.success(body.status === "archived" ? "Team archived" : body.status === "active" ? "Team unarchived" : "Team saved");
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["admin"] });
      void qc.invalidateQueries({ queryKey: ["team", team] });
      void qc.invalidateQueries({ queryKey: ["me"] });
    },
  });
}

export function AdminTeamPage() {
  const { team } = useParams({ from: "/app/admin/teams/$team" });
  const isAdmin = useIsPlatformAdmin();
  const summary = useQuery(adminTeamQuery(team));
  const [archiving, setArchiving] = useState(false);
  const t = summary.data?.team;
  const status = useTeamUpdate(team, t?.revision, () => setArchiving(false));
  const personal = isPersonal(t);
  const owner = usePersonalOwner(summary.data?.personalOwner);

  if (summary.isLoading) return <PageSkeleton />;
  if (isNotFound(summary.error)) return <NotFoundState what="team" />;
  if (!t || !summary.data) return <ErrorAlert error={summary.error} title="Couldn't load this team" />;
  const archived = t.status === "archived";
  const sum = summary.data;

  return (
    <>
      <DetailPage
        title={t.name}
        meta={
          <>
            {personal && <Badge>Personal workspace</Badge>}
            <TeamStatusBadge status={t.status} />
          </>
        }
        facts={
          personal
            ? [
                { label: "Owner", value: owner ? owner.name : undefined },
                { label: "Slug", value: <code>{t.slug}</code> },
              ]
            : [
                { label: "Slug", value: <code>{t.slug}</code> },
                { label: "Members", value: plural(sum.memberCount, "member") },
                { label: "Owners", value: plural(sum.ownerCount, "owner") },
              ]
        }
        menuActions={[
          {
            label: personal ? "Open the workspace's pages" : "Open the team's pages",
            icon: <ExternalLink aria-hidden />,
            render: <Link to="/teams/$team" params={{ team: t.slug }} />,
          },
          { label: "Open the owner", icon: <UsersRound aria-hidden />, hidden: !owner, render: <Link to="/admin/users/$userId" params={{ userId: owner?.id ?? "" }} /> },
          { label: "Unarchive team", icon: <ArchiveRestore aria-hidden />, hidden: !isAdmin || !archived, onSelect: () => status.mutate({ status: "active" }) },
          { label: "Archive team…", icon: <Archive aria-hidden />, danger: true, hidden: !isAdmin || archived, onSelect: () => setArchiving(true) },
        ]}
        notices={archived && <Alert tone="warning">{personal ? "Archived: read-only for its owner." : "Archived: read-only for its members."}</Alert>}
        tabIds={adminTeamTabs}
        tabsLabel="Team sections"
        tabs={[
          { value: "overview", label: "Overview", icon: <LayoutDashboard aria-hidden />, content: <TeamOverviewTab summary={sum} owner={owner} /> },
          // A personal workspace has only its owner: no members to manage, no owner to assign.
          personal
            ? { value: "limits", label: "Limits", icon: <Gauge aria-hidden />, content: <TeamLimitsTab team={t} isAdmin={isAdmin} /> }
            : { value: "members", label: "Members", icon: <UsersRound aria-hidden />, count: sum.memberCount, content: <TeamMembersTab team={team} isAdmin={isAdmin} /> },
          {
            value: "settings",
            label: "Settings",
            icon: <Settings2 aria-hidden />,
            content: <TeamSettingsTab team={t} isAdmin={isAdmin} onArchive={() => setArchiving(true)} status={status} />,
          },
        ]}
      />
      <AlertDialog
        open={archiving}
        onOpenChange={setArchiving}
        title={personal ? `Archive ${owner ? `${owner.name}'s` : "this"} personal workspace?` : `Archive ${t.name}?`}
        description={
          personal
            ? "The workspace becomes read-only: its owner keeps access but can't change anything. You can unarchive it later."
            : "The team becomes read-only: members keep access but can't change anything. You can unarchive it later."
        }
        confirmLabel="Archive team"
        busy={status.isPending}
        error={status.error}
        onConfirm={() => status.mutate({ status: "archived" })}
      />
    </>
  );
}
