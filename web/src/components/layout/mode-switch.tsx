import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useCurrentUser } from "../../session";
import { LayoutDashboard, Shield } from "lucide-react";
import { type ComponentPropsWithoutRef, useEffect } from "react";
import {
  SidebarModeSwitch,
  useAppShell,
} from "@/components/ui/app-shell/app-shell";
import styles from "./layout.module.css";

/*
 * Workspace / admin portal switch; each mode remembers the last page the
 * signed-in person visited in it during this browser session. The memory is
 * per person: after someone else signs in in the same tab, Admin opens on
 * Overview, not on the page the previous person left.
 */

export type Mode = "workspace" | "admin";

const lastHrefKey = (userId: string, mode: Mode) =>
  `pdp.lastHref.${userId}.${mode}`;

const defaultHref: Record<Mode, string> = { workspace: "/", admin: "/admin" };

function readLastHref(userId: string, mode: Mode) {
  try {
    return (
      globalThis.sessionStorage?.getItem(lastHrefKey(userId, mode)) ??
      defaultHref[mode]
    );
  } catch {
    return defaultHref[mode];
  }
}

const modeOfPath = (pathname: string): Mode =>
  pathname === "/admin" || pathname.startsWith("/admin/")
    ? "admin"
    : "workspace";

/** Remembers the last page visited in each mode, so switching back returns to it.
 * The mode comes from the URL itself: during a navigation the location updates
 * before the matched routes, so a route-derived mode could file a page under
 * the wrong mode. */
export function useRememberHref() {
  const userId = useCurrentUser().user.id;
  const href = useRouterState({ select: (st) => st.location.href });
  const pathname = useRouterState({ select: (st) => st.location.pathname });
  useEffect(() => {
    const mode = modeOfPath(pathname);
    try {
      globalThis.sessionStorage?.setItem(lastHrefKey(userId, mode), href);
    } catch {
      // Ignore storage errors.
    }
  }, [userId, pathname, href]);
}

/** An in-app link to a stored href (not a typed route). Modified clicks open normally. */
function HrefLink({
  href,
  ...props
}: ComponentPropsWithoutRef<"a"> & { href: string }) {
  const navigate = useNavigate();
  return (
    <a
      {...props}
      href={href}
      onClick={(e) => {
        props.onClick?.(e);
        if (
          e.defaultPrevented ||
          e.button !== 0 ||
          e.metaKey ||
          e.ctrlKey ||
          e.shiftKey ||
          e.altKey
        )
          return;
        e.preventDefault();
        void navigate({ href });
      }}
    />
  );
}

export function ModeSwitch({ mode }: { mode: Mode }) {
  const userId = useCurrentUser().user.id;
  return (
    <SidebarModeSwitch
      label="Portal"
      items={[
        {
          label: "Workspace",
          icon: <LayoutDashboard aria-hidden />,
          current: mode === "workspace",
          render: (
            <HrefLink
              href={
                mode === "workspace" ? "/" : readLastHref(userId, "workspace")
              }
            />
          ),
        },
        {
          label: "Admin",
          icon: <Shield aria-hidden />,
          current: mode === "admin",
          render: (
            <HrefLink
              href={
                mode === "admin"
                  ? defaultHref.admin
                  : readLastHref(userId, "admin")
              }
            />
          ),
        },
      ]}
    />
  );
}

/** The admin portal's name under the mode switch (auditors see the shell's one "Read-only" badge). */
export function AdminHeader() {
  const shell = useAppShell();
  const auditor = !useCurrentUser().capabilities.platformAdmin;
  // Collapsed, the mode switch's shield already marks admin mode; keep the text for screen readers.
  return (
    <div className={shell?.collapsed ? "sr-only" : styles.adminHeader}>
      <span aria-hidden className={styles.adminHeaderIcon}>
        <Shield />
      </span>
      <span className={styles.adminHeaderText}>
        <span className={styles.adminHeaderName}>
          {auditor ? "Platform auditor" : "Platform admin"}
        </span>
        <span className={styles.adminHeaderDescription}>
          {auditor
            ? "All teams and settings, read-only"
            : "All teams and settings"}
        </span>
      </span>
    </div>
  );
}
