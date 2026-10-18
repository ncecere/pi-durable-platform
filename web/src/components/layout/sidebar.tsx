import { Link, useRouterState } from "@tanstack/react-router";
import { KeyRound, LifeBuoy, LogOut, User } from "lucide-react";
import { InstanceLogo, useInstance, useSignOut, type Me } from "../../session";
import {
  Brand,
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarItem,
  SidebarNav,
  SidebarSection,
  SidebarUser,
} from "@/components/ui/app-shell/app-shell";
import {
  MenuHeader,
  MenuItem,
  MenuLinkItem,
  MenuSeparator,
} from "@/components/ui/menu/menu";
import styles from "./layout.module.css";
import { type ActiveTeam } from "./active-team";
import { useCapabilities } from "./location";
import { AdminHeader, ModeSwitch, type Mode } from "./mode-switch";
import { TeamSwitcher } from "./team-switcher";
import { ThemeMenuGroup } from "./theme";
import {
  activeAdminGroup,
  adminSections,
  teamNavFor,
  workspaceNav,
} from "./nav";
import { useAdminGroups } from "./admin-groups";

export function AppSidebar({
  me,
  mode,
  active,
}: {
  me: Me;
  mode: Mode;
  active: ActiveTeam;
}) {
  const signOut = useSignOut();
  const instance = useInstance();
  const { canAdmin } = useCapabilities(me);
  return (
    <Sidebar label={mode === "admin" ? "Admin sidebar" : "Workspace sidebar"}>
      <SidebarHeader>
        <Brand
          name={instance.name}
          logo={
            instance.logoUrl ? (
              <InstanceLogo
                src={instance.logoUrl}
                className={styles.brandLogo}
              />
            ) : undefined
          }
          render={<Link to="/" />}
        />
        {canAdmin && <ModeSwitch mode={mode} />}
        {mode === "admin" ? (
          <AdminHeader />
        ) : (
          <TeamSwitcher me={me} active={active} />
        )}
      </SidebarHeader>
      <SidebarContent>
        <SidebarNav aria-label="Main">
          {mode === "admin" ? (
            <AdminNav />
          ) : (
            <WorkspaceNav me={me} active={active} />
          )}
        </SidebarNav>
      </SidebarContent>
      <SidebarFooter>
        <SidebarUser name={me.user.displayName} email={me.user.email}>
          <MenuHeader>
            <strong>{me.user.displayName}</strong>
            {me.user.email}
          </MenuHeader>
          <MenuSeparator />
          <MenuLinkItem
            icon={<User aria-hidden />}
            render={<Link to="/settings/profile" />}
          >
            Profile
          </MenuLinkItem>
          <MenuLinkItem
            icon={<KeyRound aria-hidden />}
            render={<Link to="/settings/api-keys" />}
          >
            API keys
          </MenuLinkItem>
          <MenuSeparator />
          <ThemeMenuGroup />
          <MenuSeparator />
          {instance.supportUrl && (
            <MenuLinkItem
              href={instance.supportUrl}
              target="_blank"
              rel="noreferrer"
              icon={<LifeBuoy aria-hidden />}
            >
              Help
            </MenuLinkItem>
          )}
          <MenuItem
            icon={<LogOut aria-hidden />}
            onClick={() => signOut.mutate()}
          >
            Sign out
          </MenuItem>
        </SidebarUser>
      </SidebarFooter>
    </Sidebar>
  );
}

/** Workspace: the same shape on every page: the workspace's pages, then the active team's. */
function WorkspaceNav({ me, active }: { me: Me; active: ActiveTeam }) {
  const slug = active.slug;
  return (
    <>
      <SidebarSection label="Workspace">
        {workspaceNav.map((item) => (
          <SidebarItem
            key={item.to}
            icon={item.icon}
            label={item.label}
            render={
              <Link
                to={item.to}
                activeOptions={{ exact: item.exact ?? false }}
              />
            }
          />
        ))}
      </SidebarSection>
      {slug && (
        <SidebarSection label={active.name ?? "Team"}>
          {teamNavFor(me, slug, active.kind).map((item) => (
            <SidebarItem
              key={item.to}
              icon={item.icon}
              label={item.label}
              render={
                <Link
                  to={item.to}
                  params={{ team: slug }}
                  activeOptions={{ exact: item.exact ?? false }}
                />
              }
            />
          ))}
        </SidebarSection>
      )}
    </>
  );
}

/** Admin groups; they collapse (useAdminGroups). */
function AdminNav() {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const groups = useAdminGroups(activeAdminGroup(pathname));
  return adminSections.map((section, i) => (
    <SidebarSection
      key={section.label ?? i}
      label={section.label}
      collapsible={Boolean(section.label)}
      open={section.label ? groups.isOpen(section.label) : undefined}
      onOpenChange={
        section.label ? (o) => groups.setOpen(section.label!, o) : undefined
      }
    >
      {section.items.map((item) => (
        <SidebarItem
          key={item.to}
          icon={item.icon}
          label={item.label}
          render={
            <Link to={item.to} activeOptions={{ exact: item.exact ?? false }} />
          }
        />
      ))}
    </SidebarSection>
  ));
}
