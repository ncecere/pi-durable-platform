import { Link } from "@tanstack/react-router";
import { Home, LifeBuoy, Plus } from "lucide-react";
import { type Me } from "../../session";
import { WorkspaceSwitcher } from "@/components/ui/app-shell/app-shell";
import { Avatar } from "@/components/ui/avatar/avatar";
import { MenuGroup, MenuHeader, MenuLinkItem, MenuSeparator } from "@/components/ui/menu/menu";
import { roleLabels } from "../roles";
import { type ActiveTeam } from "./active-team";
import { useJoinTeamHelp } from "./join-team";
import styles from "./layout.module.css";

/* The sidebar's team switcher (workspace mode): shows the active team (the page's, else the last used). */

export function TeamSwitcher({ me, active }: { me: Me; active: ActiveTeam }) {
  const help = useJoinTeamHelp();
  const staff = me.capabilities.platformAdmin || me.capabilities.platformAuditor;
  // Without a team the switcher shows the person (their initials, not "NY" for "No team yet"; P-08).
  const noTeam = !active.slug;
  const name = active.name ?? (active.slug ? "Team" : me.user.displayName || me.user.email);
  const description = noTeam ? "No team yet" : active.membership ? roleLabels[active.membership.role] : staff ? "Viewing as platform staff" : undefined;
  return (
    <WorkspaceSwitcher name={name} description={description}>
      {me.teams.length > 0 ? (
        <MenuGroup label="Your teams">
          {me.teams.map((t) => (
            <MenuLinkItem
              key={t.id}
              render={<Link to="/teams/$team" params={{ team: t.slug }} />}
              icon={<Avatar name={t.name} shape="square" size="xs" decorative />}
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
            You aren't on a team yet. <span className={styles.menuTeamRole}>{help.text}</span>
          </span>
        </MenuHeader>
      )}
      {me.teams.length === 0 && help.link && (
        <MenuLinkItem href={help.link.href} target="_blank" rel="noreferrer" icon={help.link.label === "Get help" ? <LifeBuoy aria-hidden /> : <Plus aria-hidden />}>
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
