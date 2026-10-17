/* The team overview's people: counts by role, the first members, and what the viewer's role can do. */
import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Crown, PencilRuler, UsersRound } from "lucide-react";
import { membersQuery } from "../../../api/queries";
import { RoleBadge } from "../../../components/role-badge";
import { roleAbilities, roleLabels, type TeamRole } from "../../../components/roles";
import { ErrorAlert } from "@/components/ui/alert/alert";
import { Avatar } from "@/components/ui/avatar/avatar";
import { Button } from "@/components/ui/button/button";
import { Card } from "@/components/ui/card/card";
import { EmptyState } from "@/components/ui/empty-state/empty-state";
import { Item, ItemActions, ItemContent, ItemDescription, ItemGroup, ItemMedia, ItemTitle } from "@/components/ui/item/item";
import { Skeleton, SkeletonText } from "@/components/ui/skeleton/skeleton";
import { StatCard } from "@/components/ui/stat-card/stat-card";
import { plural } from "@/lib/format";
import s from "../../shared.module.css";
import { useTeam } from "../common";
import o from "./overview.module.css";

/** Members shown on the overview before "View all". */
export const OVERVIEW_MEMBERS = 6;

function useMembers() {
  const { slug } = useTeam();
  return useQuery(membersQuery(slug));
}

/** Members, owners, and admins and editors, each a stat card. */
export function PeopleCounts() {
  const { slug } = useTeam();
  const members = useMembers();
  if (members.isLoading) {
    return (
      <div className={s.stats3} role="status" aria-label="Loading members…">
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
        <Skeleton height="6rem" />
      </div>
    );
  }
  const list = members.data ?? [];
  const count = (...roles: TeamRole[]) => list.filter((m) => roles.includes(m.role)).length;
  return (
    <div className={s.stats3}>
      <StatCard label="Members" value={list.length.toLocaleString()} icon={<UsersRound />} render={<Link to="/teams/$team/settings" params={{ team: slug }} />} />
      <StatCard label="Owners" value={count("owner").toLocaleString()} icon={<Crown />} />
      <StatCard label="Admins and editors" value={count("admin", "editor").toLocaleString()} icon={<PencilRuler />} />
    </div>
  );
}

/** The first members, owners first, with "View all" to Team settings › Members. */
export function MembersCard() {
  const { slug } = useTeam();
  const members = useMembers();
  const list = members.data ?? [];
  return (
    <Card
      title="Members"
      description={members.data ? plural(list.length, "person", "people") : undefined}
      actions={
        <Button size="sm" variant="ghost" render={<Link to="/teams/$team/settings" params={{ team: slug }} aria-label="View all members" />}>
          View all <ArrowRight aria-hidden />
        </Button>
      }
    >
      {members.isLoading ? (
        <div role="status" aria-label="Loading members…">
          <SkeletonText lines={3} />
        </div>
      ) : members.error ? (
        <ErrorAlert error={members.error} title="Couldn't load the members" />
      ) : list.length === 0 ? (
        <EmptyState size="compact" icon={<UsersRound />} title="No members yet." />
      ) : (
        <ItemGroup aria-label="Members">
          {list.slice(0, OVERVIEW_MEMBERS).map((m) => (
            <Item key={m.user.id} size="xs" className={o.row}>
              <ItemMedia>
                <Avatar name={m.user.displayName || m.user.email} size="sm" decorative />
              </ItemMedia>
              <ItemContent>
                <ItemTitle>{m.user.displayName || m.user.email}</ItemTitle>
                {m.user.displayName && <ItemDescription>{m.user.email}</ItemDescription>}
              </ItemContent>
              <ItemActions>
                <span className={s.muted}>{roleLabels[m.role]}</span>
              </ItemActions>
            </Item>
          ))}
        </ItemGroup>
      )}
    </Card>
  );
}

/** What the viewer's role can and can't do, for members (who don't see the audit log). */
export function YourRoleCard({ role }: { role: TeamRole }) {
  const { can, cannot } = roleAbilities[role];
  return (
    <Card title="Your role" actions={<RoleBadge role={role} />}>
      <div className={o.abilities}>
        <p className={o.abilityHeading}>You can</p>
        <ul className={o.abilityList}>
          {can.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <p className={o.abilityHeading}>You can't</p>
        <ul className={o.abilityList}>
          {cannot.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <p className={s.note}>Ask a team admin or owner if you need a different role.</p>
      </div>
    </Card>
  );
}
