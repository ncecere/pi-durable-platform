import { Link } from "@tanstack/react-router";
import { Home, LifeBuoy, Plus, UserRound } from "lucide-react";
import { isPersonal } from "../../api/personal";
import { type Me } from "../../session";
import { WorkspaceSwitcher } from "@/components/ui/app-shell/app-shell";
import { Avatar } from "@/components/ui/avatar/avatar";
import {
  MenuGroup,
  MenuHeader,
  MenuLinkItem,
  MenuSeparator,
} from "@/components/ui/menu/menu";
import { roleLabels } from "../roles";
import { type ActiveTeam } from "./active-team";
import { useJoinTeamHelp } from "./join-team";
import styles from "./layout.module.css";

/*
 * The sidebar's team switcher (workspace mode): shows the active team (the page's, else the last used). The
 * person's personal workspace comes first, on its own, then the shared teams they're on.
 */

export function TeamSwitcher({ me, active }: { me: Me; active: ActiveTeam }) {
  const help = useJoinTeamHelp();
  const staff =
    me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  const personal = me.teams.find(isPersonal);
  const shared = me.teams.filter((t) => !isPersonal(t));
  // Without a team the switcher shows the person (their initials, not "NY" for "No team yet"; P-08).
  const noTeam = !active.slug;
  const name =
    active.name ??
    (active.slug ? "Team" : me.user.displayName || me.user.email);
  const description = noTeam
    ? "No team yet"
    : active.membership && isPersonal(active.membership)
      ? "Your personal workspace"
      : active.membership
        ? roleLabels[active.membership.role]
        : staff
          ? "Viewing as platform staff"
          : undefined;
  return (
    <WorkspaceSwitcher name={name} description={description}>
      {personal && (
        <>
          <MenuLinkItem
            render={<Link to="/teams/$team" params={{ team: personal.slug }} />}
            icon={<UserRound aria-hidden />}
          >
            <span className={styles.menuTeam}>
              <span className={styles.menuTeamName}>Personal</span>
              <span className={styles.menuTeamRole}>
                Just you
                {personal.status === "archived" && " · Archived"}
              </span>
            </span>
          </MenuLinkItem>
          <MenuSeparator />
        </>
      )}
      {shared.length > 0 ? (
        <MenuGroup label="Your teams">
          {shared.map((t) => (
            <MenuLinkItem
              key={t.id}
              render={<Link to="/teams/$team" params={{ team: t.slug }} />}
              icon={
                <Avatar name={t.name} shape="square" size="xs" decorative />
              }
            >
              <span className={styles.menuTeam}>
                <span className={styles.menuTeamName}>{t.name}</span>
                <span className={styles.menuTeamRole}>
                  {roleLabels[t.role]}
                  {t.status === "archived" && " · Archived"}
                </span>
              </span>
            </MenuLinkItem>
          ))}
        </MenuGroup>
      ) : (
        <MenuHeader>
          <span className={styles.menuNoTeam}>
            {personal
              ? "You aren't on a shared team yet."
              : "You aren't on a team yet."}{" "}
            <span className={styles.menuTeamRole}>{help.text}</span>
          </span>
        </MenuHeader>
      )}
      {shared.length === 0 && help.link && (
        <MenuLinkItem
          href={help.link.href}
          target="_blank"
          rel="noreferrer"
          icon={
            help.link.label === "Get help" ? (
              <LifeBuoy aria-hidden />
            ) : (
              <Plus aria-hidden />
            )
          }
        >
          {help.link.label}
        </MenuLinkItem>
      )}
      <MenuSeparator />
      <MenuLinkItem render={<Link to="/" />} icon={<Home aria-hidden />}>
        Home
      </MenuLinkItem>
    </WorkspaceSwitcher>
  );
}
