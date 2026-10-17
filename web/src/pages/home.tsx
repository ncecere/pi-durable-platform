/*
 * Home: a welcome, where agents will live, your teams as compact rows with
 * quick links, and, for platform staff, the way into the admin portal.
 * Someone without a team gets a friendly explanation of what teams are and
 * how to get onto one, with "Request a new team" only when the instance has
 * a request address.
 */
import { Link } from "@tanstack/react-router";
import { ArrowRight, Shield, Sparkles, Users } from "lucide-react";
import { roleLabels } from "../components/roles";
import { useJoinTeamHelp } from "../components/layout/join-team";
import { terms } from "../lib/terms";
import { type Me, useCurrentUser, useInstance } from "../session";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Badge } from "@/components/ui/badge/badge";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Item, ItemActions, ItemContent, ItemGroup, ItemMedia, ItemTitle } from "@/components/ui/item/item";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import s from "./shared.module.css";
import styles from "./home.module.css";

export function HomePage() {
  const me = useCurrentUser();
  const instance = useInstance();
  const firstName = me.user.displayName.split(" ")[0] || me.user.displayName || me.user.email;
  const hasTeam = me.teams.length > 0;
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;

  return (
    <Stack gap={6} className={s.page}>
      <PageHeader
        title={`Welcome, ${firstName}`}
        description={
          hasTeam
            ? `${instance.name} is where your teams and services deploy and manage Pi Durable agents.`
            : `${instance.name} is where teams and services deploy and manage Pi Durable agents. Join a team to get started.`
        }
      />
      <AgentsCard />
      <YourTeams me={me} />
      {staff && <PlatformCard platformAdmin={me.capabilities.platformAdmin} />}
    </Stack>
  );
}

function YourTeams({ me }: { me: Me }) {
  const help = useJoinTeamHelp();
  if (me.teams.length === 0) {
    return (
      <Card title="Your teams">
        <EmptyState
          size="compact"
          icon={<Users />}
          title="You aren't on a team yet."
          description={`Teams own agents and service accounts, and decide who may use them. ${help.text}`}
          action={
            help.link && (
              <Button variant="secondary" render={<a href={help.link.href} target="_blank" rel="noreferrer" />}>
                {help.link.label}
              </Button>
            )
          }
        />
      </Card>
    );
  }
  return (
    <Card title="Your teams" description={me.teams.length === 1 ? "1 team" : `${me.teams.length} teams`}>
      <ItemGroup aria-label="Your teams">
        {me.teams.map((t) => (
          <Item key={t.id} variant="outline" size="sm" className={styles.teamRow}>
            <ItemMedia>
              <Avatar name={t.name} shape="square" size="md" decorative />
            </ItemMedia>
            <ItemContent className={styles.teamContent}>
              <ItemTitle>
                <Link to="/teams/$team" params={{ team: t.slug }} className={styles.teamLink}>
                  {t.name}
                </Link>
              </ItemTitle>
              {/* Not an ItemDescription: that clamps its lines and cut the badge off on a phone. */}
              <div className={styles.teamBadges}>
                <Badge tone="info" size="sm">
                  {roleLabels[t.role]}
                </Badge>
                {t.status === "archived" && (
                  <Badge tone="warning" size="sm">
                    Archived
                  </Badge>
                )}
              </div>
            </ItemContent>
            <ItemActions className={styles.quickLinks}>
              <Button size="sm" variant="ghost" render={<Link to="/teams/$team" params={{ team: t.slug }} aria-label={`${t.name}: overview`} />}>
                Overview
              </Button>
              <Button size="sm" variant="ghost" render={<Link to="/teams/$team/settings" params={{ team: t.slug }} aria-label={`${t.name}: ${terms.teamSettings.toLowerCase()}`} />}>
                {terms.teamSettings}
              </Button>
            </ItemActions>
          </Item>
        ))}
      </ItemGroup>
    </Card>
  );
}

/** Where agents will live (the Agents page). */
function AgentsCard() {
  return (
    <Card
      title={terms.agents}
      description="Each agent runs in its own isolated Pi Durable runtime, for example one assistant per user of a chat app."
      actions={
        <Button size="sm" variant="ghost" render={<Link to="/agents" />}>
          <Sparkles aria-hidden /> Open {terms.agents} <ArrowRight aria-hidden />
        </Button>
      }
    />
  );
}

/** Platform admins and auditors: where the platform is managed. */
function PlatformCard({ platformAdmin }: { platformAdmin: boolean }) {
  return (
    <Card
      title="Platform administration"
      description={
        platformAdmin
          ? "Manage users, teams and their owners, and read the audit log."
          : "See users, teams and the audit log, read-only."
      }
      actions={
        <Button size="sm" variant="ghost" render={<Link to="/admin" />}>
          <Shield aria-hidden /> Open Admin <ArrowRight aria-hidden />
        </Button>
      }
    >
      <div className={styles.adminLinks}>
        <Button size="sm" variant="secondary" render={<Link to="/admin/users" />}>
          Users
        </Button>
        <Button size="sm" variant="secondary" render={<Link to="/admin/teams" />}>
          Teams
        </Button>
        <Button size="sm" variant="secondary" render={<Link to="/admin/audit" />}>
          {terms.auditLog}
        </Button>
      </div>
    </Card>
  );
}
