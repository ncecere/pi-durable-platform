/*
 * Profile (/settings/profile), from the account menu: who the person is as the portal knows them, their teams and
 * their SSO groups. It is reachable for everyone, including people in no team. Nothing here is edited: the name, email
 * and groups come from the sign-in.
 */
import { Link } from "@tanstack/react-router";
import { RoleBadge } from "@/components/role-badge";
import { Badge } from "@/components/ui/badge/badge";
import { Card } from "@/components/ui/card/card";
import { Stack } from "@/components/ui/layout/layout";
import { PageHeader } from "@/components/ui/page-header/page-header";
import { TextLink } from "@/components/ui/text-link/text-link";
import { Time } from "@/components/ui/time/time";
import { useJoinTeamHelp } from "@/components/layout/join-team";
import { useCurrentUser, useInstance } from "@/session";
import s from "../shared.module.css";
import { platformRoleLabels } from "../admin/people/common";
import a from "./account.module.css";

export function ProfilePage() {
  const me = useCurrentUser();
  const instance = useInstance();
  const help = useJoinTeamHelp();
  const { user, teams, groups } = me;
  return (
    <Stack gap={6} className={s.page}>
      <PageHeader title="Profile" description={`Your account in ${instance.name}. Your name and email come from how you sign in.`} />
      <Card title="You">
        <dl className={a.facts}>
          <dt>Name</dt>
          <dd>{user.displayName}</dd>
          <dt>Email</dt>
          <dd>{user.email}</dd>
          {user.platformRole !== "none" && (
            <>
              <dt>Platform role</dt>
              <dd>{platformRoleLabels[user.platformRole]}</dd>
            </>
          )}
          <dt>Member since</dt>
          <dd>
            <Time value={user.createdAt} format="date" />
          </dd>
          {user.lastLoginAt && (
            <>
              <dt>Last sign-in</dt>
              <dd>
                <Time value={user.lastLoginAt} />
              </dd>
            </>
          )}
        </dl>
      </Card>
      <Card title="Your teams">
        {teams.length === 0 ? (
          <p className={s.muted}>You aren't on a team yet. {help.text}</p>
        ) : (
          <ul className={a.rows}>
            {teams.map((t) => (
              <li key={t.id} className={a.row}>
                <span className={a.rowMain}>
                  <TextLink render={<Link to="/teams/$team" params={{ team: t.slug }} />}>{t.name}</TextLink>
                  {t.status === "archived" && <Badge size="sm">Archived</Badge>}
                </span>
                <span className={a.rowMeta}>
                  <RoleBadge role={t.role} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
      <Card title="Your groups" description="From your identity provider, as of your last sign-in.">
        {groups.length === 0 ? (
          <p className={s.muted}>Your identity provider didn't send any groups.</p>
        ) : (
          <ul className={a.chips} aria-label="Your groups">
            {groups.map((g) => (
              <li key={g}>
                <Badge size="sm">{g}</Badge>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Stack>
  );
}
