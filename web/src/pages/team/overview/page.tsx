/*
 * The team overview: the team's name and description, the viewer's role,
 * its people at a glance and, for editors and above (and platform staff),
 * the recent changes. Members get what their role can do instead. Members,
 * invites, the audit log and the team's details are in Team settings. The
 * agents domain adds the team's agents here. A personal workspace shows its
 * limits instead of its people (it has only its owner).
 */
import { TextLink } from "@/components/ui/text-link/text-link";
import { Link } from "@tanstack/react-router";
import { Settings, UserPlus } from "lucide-react";
import { RoleBadge } from "../../../components/role-badge";
import { PageActions } from "../../../components/templates/page-actions";
import { requestIntent } from "../../../lib/intents";
import { terms } from "../../../lib/terms";
import { useCurrentUser } from "../../../session";
import { Alert } from "@/components/ui/alert/alert";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import s from "../../shared.module.css";
import { canReadAudit, useTeam } from "../common";
import { ArchivedNotice } from "../layout";
import { LimitsCard } from "./limits";
import { MembersCard, PeopleCounts, YourRoleCard } from "./people";
import { RecentChanges } from "./recent";
import o from "./overview.module.css";

export function TeamOverviewPage() {
  const { slug, team, role, archived, isManager, personal } = useTeam();
  const me = useCurrentUser();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  // Editors and above read the team's audit log (an archived team's too); so do platform staff.
  const canAudit = canReadAudit(role, staff);

  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={team.name}
        description={team.description || (personal ? (role ? "Your own workspace: deploy agents from the catalog and use them on your own." : "One person's own workspace.") : undefined)}
        meta={
          <>
            {role && !personal && <RoleBadge role={role} prefix="Your role: " tone="info" />}
            {personal && <Badge tone="neutral">Personal workspace</Badge>}
            {archived && <Badge tone="warning">Archived (read-only)</Badge>}
          </>
        }
        actions={
          <PageActions
            secondary={[{ label: personal ? "Settings" : terms.teamSettings, icon: <Settings aria-hidden />, render: <Link to="/teams/$team/settings" params={{ team: slug }} /> }]}
            primary={
              isManager && !archived && !personal ? (
                <Button render={<Link to="/teams/$team/settings" params={{ team: slug }} />} onClick={() => requestIntent("add-member")}>
                  <UserPlus aria-hidden /> Add member
                </Button>
              ) : undefined
            }
          />
        }
      />
      <ArchivedNotice />
      {!role && personal && (
        <Alert tone="info" title="You're viewing a personal workspace as platform staff">
          You can see its agents, usage and audit log, but not its conversations.{" "}
          <TextLink render={<Link to="/admin/teams/$team" params={{ team: slug }} />}>Its limits are in Admin</TextLink>.
        </Alert>
      )}
      {!role && !personal && (
        <Alert tone="info" title="You're viewing this team as platform staff">
          You can see its members, audit log and details in Team settings.{" "}
          <TextLink render={<Link to="/admin/teams/$team" params={{ team: slug }} />}>Manage the team in Admin</TextLink>.
        </Alert>
      )}
      {personal ? (
        <div className={o.columns}>
          <LimitsCard />
          {canAudit ? <RecentChanges /> : null}
        </div>
      ) : (
        <>
          <PeopleCounts />
          <div className={o.columns}>
            <MembersCard />
            {canAudit ? <RecentChanges /> : role ? <YourRoleCard role={role} /> : null}
          </div>
        </>
      )}
    </Stack>
  );
}
