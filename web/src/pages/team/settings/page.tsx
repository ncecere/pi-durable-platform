/*
 * Team settings: Members (with open invites) · Audit log · General, as pill
 * tabs (?tab=). General holds the
 * team's details and the Danger zone (leave the team; platform admins
 * archive it). Later phases add tabs here in lib/tabs.ts and below.
 */
import { Link } from "@tanstack/react-router";
import { FileClock, Settings, Users } from "lucide-react";
import { PageTabs, useUrlTab } from "../../../components/page-tabs";
import { teamSettingsTabs } from "../../../lib/tabs";
import { terms } from "../../../lib/terms";
import { useCurrentUser } from "../../../session";
import { Alert } from "@/components/ui/alert/alert";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { TextLink } from "@/components/ui/text-link/text-link";
import s from "../../shared.module.css";
import { canReadAudit, useTeam } from "../common";
import { ArchivedNotice } from "../layout";
import { TeamAuditLog } from "./audit";
import { GeneralTab } from "./general";
import { MembersTab } from "./members";

/** The page's description: what its tabs hold for this reader. */
export function settingsDescription(name: string, canAudit: boolean) {
  return canAudit
    ? `The members, open invites, audit log and general details of ${name}.`
    : `The members and general details of ${name}.`;
}

export function TeamSettingsPage() {
  const { slug, team, role } = useTeam();
  const me = useCurrentUser();
  const [tab, setTab] = useUrlTab(teamSettingsTabs);
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  const canAudit = canReadAudit(role, staff);

  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title={terms.teamSettings} description={settingsDescription(team.name, canAudit)} />
      <ArchivedNotice />
      {!role && (
        <Alert tone="info" title="You're viewing this team as platform staff">
          You can see its members, audit log and details. Owners are assigned on its{" "}
          <TextLink render={<Link to="/admin/teams/$team" params={{ team: slug }} />}>admin page</TextLink>.
        </Alert>
      )}
      <PageTabs
        label="Team settings sections"
        value={tab}
        onValueChange={setTab}
        crumb="always"
        tabs={[
          { value: "members", label: "Members", icon: <Users aria-hidden />, content: <MembersTab /> },
          { value: "audit", label: terms.auditLog, icon: <FileClock aria-hidden />, hidden: !canAudit, content: <TeamAuditLog /> },
          { value: "general", label: "General", icon: <Settings aria-hidden />, content: <GeneralTab /> },
        ]}
      />
    </Stack>
  );
}
