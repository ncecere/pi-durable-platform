/*
 * The signed-in app shell: sidebar (brand, mode switch, team switcher,
 * navigation, user menu), top bar (sidebar toggle, breadcrumbs, command
 * palette) and the main content area.
 */
import { Outlet } from "@tanstack/react-router";
import { type ReactNode, useCallback, useEffect, useState } from "react";
import { terms } from "../../lib/terms";
import { useCurrentUser, useInstance } from "../../session";
import { AppShell, Main, TopBar } from "@/components/ui/app-shell/app-shell";
import { Badge } from "@/components/ui/badge/badge";
import { Breadcrumbs } from "@/components/ui/breadcrumbs/breadcrumbs";
import { CommandPalette, CommandPaletteTrigger, useCommandPaletteShortcut } from "@/components/ui/command-palette/command-palette";
import { TooltipProvider } from "@/components/ui/tooltip/tooltip";
import { useMediaQuery } from "@/lib/bitop-utils";
import styles from "./layout.module.css";
import { useActiveTeam } from "./active-team";
import { isAdminRoute, useCapabilities, useLocationInfo } from "./location";
import { documentTitle, fitCrumbs, useBreadcrumbs } from "./breadcrumbs";
import { useCurrentPageCrumbs } from "./crumb-tail";
import { TakeoverHost } from "../templates/takeover";
import { PALETTE_LABEL, useCommands } from "./commands";
import { type Mode, useRememberHref } from "./mode-switch";
import { RateLimitBanner } from "./rate-limit-banner";
import { AppSidebar } from "./sidebar";

const collapsedKey = "pdp.sidebarCollapsed";

/**
 * Below this width the sidebar starts as the icon rail, so lists and tables
 * get the room. It follows the window until the person chooses.
 */
export const COMPACT_SHELL_QUERY = "(max-width: 68.75rem)";

/** The person's own choice (the toggle), or null to follow the window's width. */
function savedCollapsed(): boolean | null {
  try {
    const saved = globalThis.localStorage?.getItem(collapsedKey);
    if (saved !== null && saved !== undefined) return saved === "1";
  } catch {
    // Storage can be unavailable (private mode); fall through.
  }
  return null;
}

/** The signed-in layout. Renders the matched route, or `children` (e.g. the not-found page). */
export function AppLayout({ children }: { children?: ReactNode }) {
  const me = useCurrentUser();
  const loc = useLocationInfo();
  const { canAdmin, readOnlyAdmin } = useCapabilities(me);
  const [choice, setChoice] = useState(savedCollapsed);
  const compact = useMediaQuery(COMPACT_SHELL_QUERY);
  const collapsed = choice ?? compact;
  const [paletteOpen, setPaletteOpen] = useState(false);
  const togglePalette = useCallback(() => setPaletteOpen((o) => !o), []);
  useCommandPaletteShortcut(togglePalette);

  const active = useActiveTeam(me, loc);
  const crumbs = useBreadcrumbs(loc, canAdmin);
  const narrow = useMediaQuery("(max-width: 37.5rem)");
  // The bottom record or form page's back link names the route's page underneath.
  const pageCrumbs = useCurrentPageCrumbs();
  const under = pageCrumbs.length > 0 ? crumbs[crumbs.length - 1 - pageCrumbs.length]?.label : undefined;
  const backLabel = typeof under === "string" ? under : undefined;
  const commands = useCommands(me, active);
  // Only admins and auditors get the admin shell; others see the workspace shell around the no-access page.
  const inAdmin = canAdmin && isAdminRoute(loc.routeId);
  const instanceName = useInstance().name;
  const title = documentTitle(crumbs, instanceName);
  useEffect(() => {
    document.title = title;
  }, [title]);
  const mode: Mode = inAdmin ? "admin" : "workspace";
  useRememberHref();

  return (
    <TooltipProvider>
      <AppShell
        collapsed={collapsed}
        onCollapsedChange={(c) => {
          setChoice(c);
          try {
            globalThis.localStorage?.setItem(collapsedKey, c ? "1" : "0");
          } catch {
            // Ignore storage errors.
          }
        }}
        sidebar={<AppSidebar me={me} mode={mode} active={active} />}
        topbar={
          <TopBar
            start={<Breadcrumbs items={fitCrumbs(crumbs, narrow)} className={styles.crumbs} />}
            end={
              <>
                {inAdmin && readOnlyAdmin && (
                  // On a phone only the dot shows (the word stays for screen readers), so the search button fits.
                  <Badge tone="warning" dot className={styles.adminBadge} title={terms.readOnly}>
                    <span className={styles.adminBadgeText}>{terms.readOnly}</span>
                  </Badge>
                )}
                <CommandPaletteTrigger onClick={() => setPaletteOpen(true)} label="Search or jump to…" className={styles.search} />
              </>
            }
          />
        }
      >
        <Main>
          <RateLimitBanner />
          <TakeoverHost backLabel={backLabel}>{children ?? <Outlet />}</TakeoverHost>
        </Main>
      </AppShell>
      <CommandPalette
        open={paletteOpen}
        onOpenChange={setPaletteOpen}
        groups={commands.groups}
        label={PALETTE_LABEL}
        placeholder={canAdmin ? "Search pages, teams and admin pages…" : "Search pages, teams and actions…"}
        emptyText="No results found."
      />
    </TooltipProvider>
  );
}
