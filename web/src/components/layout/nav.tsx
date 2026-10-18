/*
 * The sidebar's navigation, as data: the workspace's own pages, the active
 * team's pages and the admin portal's groups. The sidebar, the breadcrumbs
 * and the ⌘K palette all read these lists, so a page added here shows up in
 * all three.
 *
 * Adding a section:
 *   1. add its route in router.tsx (the `to` paths below are typed against
 *      the route tree, so an item can't point at a page that doesn't exist);
 *   2. add an item to `workspaceNav` (pages for everyone: Agents),
 *      `teamNav` (the team's own pages, with an `access` rule) or a group of
 *      `adminSections`;
 *   3. add its breadcrumb in breadcrumbs.tsx.
 * Only list pages that exist.
 */
import {
  BookOpen,
  Bot,
  ChartColumn,
  Cpu,
  FileClock,
  Home,
  LayoutDashboard,
  Settings,
  Sparkles,
  Unplug,
  Users,
  UsersRound,
} from "lucide-react";
import { type ReactNode } from "react";
import { terms } from "../../lib/terms";
import { type Me } from "../../session";

export const icon = (C: typeof Home) => <C aria-hidden />;

/* ---------------- workspace ---------------- */

export type WorkspacePath = "/" | "/agents" | "/catalog";

export type WorkspaceNavItem = {
  to: WorkspacePath;
  label: string;
  icon: ReactNode;
  exact?: boolean;
  keywords?: string[];
};

/** Pages for everyone signed in, at the top of the workspace sidebar. */
export const workspaceNav: WorkspaceNavItem[] = [
  {
    to: "/",
    label: terms.home,
    icon: icon(Home),
    exact: true,
    keywords: ["start", "welcome", "teams"],
  },
  {
    to: "/agents",
    label: "All agents",
    icon: icon(Sparkles),
    keywords: [
      "agent",
      "agents",
      "every team",
      "runtime",
      "deploy",
      "pi durable",
    ],
  },
  {
    to: "/catalog",
    label: "Catalog",
    icon: icon(BookOpen),
    keywords: ["templates", "catalog", "deploy", "published"],
  },
];

/* ---------------- team ---------------- */

type Membership = Me["teams"][number];

/** Who may see what in a team (the same rules as the pages and the server). */
export type TeamAccess = {
  /** On the team (any role). */
  member: boolean;
  /** An active team's editors, admins and owners. */
  canEdit: boolean;
  /** An active team's admins and owners. */
  isManager: boolean;
  /** A platform admin or auditor (may view any team's overview, settings and service accounts). */
  staff: boolean;
};

export function teamAccess(me: Me, mine: Membership | undefined): TeamAccess {
  const active = mine?.status === "active";
  const role = mine?.role;
  const isManager = active && (role === "owner" || role === "admin");
  return {
    member: Boolean(mine),
    canEdit: isManager || (active && role === "editor"),
    isManager,
    staff: me.capabilities.platformAdmin || me.capabilities.platformAuditor,
  };
}

export type TeamPath =
  | "/teams/$team"
  | "/teams/$team/agents"
  | "/teams/$team/templates"
  | "/teams/$team/usage"
  | "/teams/$team/service-accounts"
  | "/teams/$team/settings";

export type TeamNavItem = {
  to: TeamPath;
  label: string;
  icon: ReactNode;
  exact?: boolean;
  /** Who gets the item. Platform staff who aren't members get the overview, the service accounts and settings. */
  access: (a: TeamAccess) => boolean;
  /** Extra command-palette search words. */
  keywords?: string[];
};

const teamNav: TeamNavItem[] = [
  {
    to: "/teams/$team",
    label: "Overview",
    icon: icon(LayoutDashboard),
    exact: true,
    access: (a) => a.member || a.staff,
    keywords: ["team", "dashboard"],
  },
  {
    to: "/teams/$team/agents",
    label: terms.agents,
    icon: icon(Sparkles),
    // Agents and their conversations are the team's own: platform staff don't get them.
    access: (a) => a.member,
    keywords: [
      "agent",
      "agents",
      "deploy",
      "test",
      "chat",
      "runtime",
      "pi durable",
      "hibernate",
    ],
  },
  {
    to: "/teams/$team/templates",
    label: "Templates",
    icon: icon(BookOpen),
    access: (a) => a.member || a.staff,
    keywords: [
      "template",
      "templates",
      "instructions",
      "version",
      "publish",
      "catalog",
    ],
  },
  {
    to: "/teams/$team/usage",
    label: "Usage",
    icon: icon(ChartColumn),
    access: (a) => a.member || a.staff,
    keywords: ["usage", "tokens", "cost", "spend", "requests", "billing", "models"],
  },
  {
    to: "/teams/$team/service-accounts",
    label: terms.serviceAccounts,
    icon: icon(Bot),
    access: (a) => a.member || a.staff,
    keywords: [
      "service account",
      "bot",
      "robot",
      "automation",
      "ci",
      "headless",
      "service",
      "script",
      "key",
      "keys",
      "rotate",
      "pdp_sa",
    ],
  },
  {
    to: "/teams/$team/settings",
    label: terms.teamSettings,
    icon: icon(Settings),
    access: (a) => a.member || a.staff,
    keywords: ["members", "invites", "audit", "general", "leave"],
  },
];

/** The team pages the user can open in the team at `slug`. */
export function teamNavFor(me: Me, slug: string): TeamNavItem[] {
  const a = teamAccess(
    me,
    me.teams.find((t) => t.slug === slug),
  );
  return teamNav.filter((item) => item.access(a));
}

/* ---------------- admin ---------------- */

export type AdminPath =
  | "/admin/users"
  | "/admin/teams"
  | "/admin/templates"
  | "/admin/models"
  | "/admin/mcp-servers"
  | "/admin/usage"
  | "/admin/audit";

export type AdminNavItem = {
  to: AdminPath;
  label: string;
  icon: ReactNode;
  exact?: boolean;
};

/**
 * Admin sidebar groups, each with the pages it holds. New areas (agents, settings) add groups here.
 * `defaultOpen`: shown open until the person closes it (see admin-groups.ts).
 */
export const adminSections: {
  label?: string;
  defaultOpen?: boolean;
  items: AdminNavItem[];
}[] = [
  {
    label: "People",
    items: [
      { to: "/admin/users", label: "Users", icon: icon(Users) },
      { to: "/admin/teams", label: "Teams", icon: icon(UsersRound) },
    ],
  },
  {
    label: "Records",
    defaultOpen: true,
    items: [
      { to: "/admin/templates", label: "Templates", icon: icon(BookOpen) },
      { to: "/admin/models", label: "Models", icon: icon(Cpu) },
      { to: "/admin/mcp-servers", label: "MCP servers", icon: icon(Unplug) },
      { to: "/admin/usage", label: "Usage", icon: icon(ChartColumn) },
      { to: "/admin/audit", label: terms.auditLog, icon: icon(FileClock) },
    ],
  },
];

export const adminNav = adminSections.flatMap((section) => section.items);

/** The label of the admin group holding the page at `pathname`. */
export function activeAdminGroup(pathname: string): string | undefined {
  const inside = (to: string, exact?: boolean) =>
    pathname === to || (!exact && pathname.startsWith(to + "/"));
  return adminSections.find((s) => s.items.some((i) => inside(i.to, i.exact)))
    ?.label;
}

/** Extra command-palette search words for admin pages. */
export const adminKeywords: Partial<Record<AdminPath, string[]>> = {
  "/admin/users": [
    "people",
    "person",
    "suspend",
    "reactivate",
    "platform role",
    "platform admin",
    "auditor",
  ],
  "/admin/teams": ["team", "create team", "archive", "owner", "assign owner"],
  "/admin/templates": [
    "templates",
    "review",
    "approve",
    "reject",
    "catalog",
    "publish",
    "platform template",
  ],
  "/admin/models": [
    "models",
    "providers",
    "llm",
    "openai",
    "anthropic",
    "litellm",
    "api key",
    "discover",
    "publish",
  ],
  "/admin/mcp-servers": ["mcp", "tools", "tool servers", "headers", "publish"],
  "/admin/usage": ["usage", "tokens", "cost", "spend", "requests", "billing", "teams", "models"],
  "/admin/audit": [
    "audit log",
    "history",
    "changes",
    "who changed",
    "sign-ins",
    "export",
    "csv",
  ],
};
