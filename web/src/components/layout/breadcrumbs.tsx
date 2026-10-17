import { useQuery } from "@tanstack/react-query";
import { Link, useRouter } from "@tanstack/react-router";
import {
  BookOpen,
  Boxes,
  Home,
  KeyRound,
  Shield,
  Sparkles,
  User,
} from "lucide-react";
import { templateQuery } from "../../api/templates";
import { agentQuery } from "../../api/agents";
import {
  adminTeamQuery,
  adminUserQuery,
  serviceAccountQuery,
  teamQuery,
} from "../../api/queries";
import { terms } from "../../lib/terms";
import { type BreadcrumbItem } from "@/components/ui/breadcrumbs/breadcrumbs";
import { VisuallyHidden } from "@/components/ui/visually-hidden/visually-hidden";
import { isNotFound } from "../not-found";
import {
  type PageCrumb,
  useCurrentCrumbTail,
  useCurrentPageCrumbs,
} from "./crumb-tail";
import { isAdminRoute, isTeamRoute, type Location } from "./location";
import { adminNav, icon, teamNavFor } from "./nav";
import { useCurrentUser } from "../../session";

/** Labels for the current route, using data the pages have already loaded, plus the page's tail (its active tab). */
export function useBreadcrumbs(
  loc: Location,
  canAdmin: boolean,
): BreadcrumbItem[] {
  const trail = useTrail(loc, canAdmin);
  const tail = useCurrentCrumbTail();
  const pages = useCurrentPageCrumbs();
  const router = useRouter();
  let crumbs = trail;
  if (tail) {
    // The last crumb becomes a link back to the page's first tab.
    const last = trail[trail.length - 1];
    const linked =
      last && !last.render
        ? { ...last, render: <Link to="." search={{}} /> }
        : last;
    crumbs = [
      ...trail.slice(0, -1),
      ...(linked ? [linked] : []),
      { label: tail },
    ];
  }
  if (pages.length === 0) return crumbs;
  // Record and form pages are open on top (bottom first). The crumb under the
  // top page closes it (Back); lower ones go straight to their page.
  const top = pages.length - 1;
  const linkTo = (
    item: BreadcrumbItem,
    above: PageCrumb,
    underTop: boolean,
  ): BreadcrumbItem => ({
    ...item,
    render: (
      <a
        href={above.href}
        onClick={(e) => {
          if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
          e.preventDefault();
          if (underTop) above.close();
          else router.history.push(above.href);
        }}
      />
    ),
  });
  const under = crumbs[crumbs.length - 1];
  const out: BreadcrumbItem[] = crumbs.slice(0, -1);
  if (under) out.push(linkTo(under, pages[0]!, top === 0));
  pages.forEach((p, i) =>
    out.push(
      i < top
        ? linkTo({ label: p.label }, pages[i + 1]!, i + 1 === top)
        : { label: p.label },
    ),
  );
  return out;
}

function useTrail(
  { routeId, params }: Location,
  canAdmin: boolean,
): BreadcrumbItem[] {
  const me = useCurrentUser();
  const slug = isTeamRoute(routeId) ? params.team : undefined;
  const team = useQuery({ ...teamQuery(slug ?? ""), enabled: !!slug });
  const userId =
    routeId === "/app/admin/users/$userId" ? params.userId : undefined;
  const user = useQuery({
    ...adminUserQuery(userId ?? ""),
    enabled: canAdmin && !!userId,
  });
  const adminTeamSlug =
    routeId === "/app/admin/teams/$team" ? params.team : undefined;
  const adminTeam = useQuery({
    ...adminTeamQuery(adminTeamSlug ?? ""),
    enabled: canAdmin && !!adminTeamSlug,
  });
  // A service account's name, likewise.
  const serviceAccountId =
    routeId === "/app/teams/$team/service-accounts/$serviceAccountId"
      ? params.serviceAccountId
      : undefined;
  const serviceAccount = useQuery({
    ...serviceAccountQuery(slug ?? "", serviceAccountId ?? ""),
    enabled: !!slug && !!serviceAccountId,
  });
  // A template's name, likewise.
  const templateId =
    routeId === "/app/templates/$templateId" ? params.templateId : undefined;
  const template = useQuery({
    ...templateQuery(templateId ?? ""),
    enabled: !!templateId,
  });
  // A team agent's name, likewise.
  const agentId =
    routeId === "/app/teams/$team/agents/$agentId" ? params.agentId : undefined;
  const agent = useQuery({ ...agentQuery(agentId ?? ""), enabled: !!agentId });
  if (routeId === "/app/") return [{ label: terms.home, icon: icon(Home) }];
  // From the account menu: the page itself, not "Home".
  if (routeId === "/app/settings/profile")
    return [{ label: "Profile", icon: icon(User) }];
  if (routeId === "/app/settings/api-keys")
    return [{ label: terms.apiKeys, icon: icon(KeyRound) }];
  if (routeId === "/app/agents")
    return [{ label: "All agents", icon: icon(Sparkles) }];
  if (routeId === "/app/catalog")
    return [{ label: "Catalog", icon: icon(BookOpen) }];
  if (routeId === "/app/templates/$templateId") {
    const owner = template.data?.teamSlug;
    return [
      owner
        ? {
            label: template.data?.teamName ?? owner,
            icon: icon(Boxes),
            render: (
              <Link to="/teams/$team/templates" params={{ team: owner }} />
            ),
          }
        : {
            label: "Catalog",
            icon: icon(BookOpen),
            render: <Link to="/catalog" />,
          },
      ...(isNotFound(template.error)
        ? []
        : [
            {
              label: template.data?.name ?? (
                <VisuallyHidden>Template</VisuallyHidden>
              ),
            },
          ]),
    ];
  }

  if (slug && isNotFound(team.error))
    return [{ label: terms.home, icon: icon(Home), render: <Link to="/" /> }];
  if (slug) {
    const crumbs: BreadcrumbItem[] = [
      // Until the team loads, its crumb has no visible text and stays out of the document title (not "… · Team · …").
      {
        label: team.data?.team.name ?? <VisuallyHidden>Team</VisuallyHidden>,
        icon: icon(Boxes),
        render: <Link to="/teams/$team" params={{ team: slug }} />,
      },
    ];
    const sub = routeId.slice("/app/teams/$team".length);
    if (sub === "/agents/$agentId") {
      crumbs.push({
        label: terms.agents,
        render: <Link to="/teams/$team/agents" params={{ team: slug }} />,
      });
      if (!isNotFound(agent.error))
        crumbs.push({
          label: agent.data?.name ?? <VisuallyHidden>Agent</VisuallyHidden>,
        });
      return crumbs;
    }
    if (sub === "/service-accounts/$serviceAccountId") {
      crumbs.push({
        label: terms.serviceAccounts,
        render: (
          <Link to="/teams/$team/service-accounts" params={{ team: slug }} />
        ),
      });
      if (!isNotFound(serviceAccount.error))
        crumbs.push({
          label: serviceAccount.data?.name ?? (
            <VisuallyHidden>Service account</VisuallyHidden>
          ),
        });
      return crumbs;
    }
    // The team page's own label from the navigation ("Overview", "Team settings"); an unknown page adds nothing (the not-found tail follows).
    const page = teamNavFor(me, slug).find(
      (n) => n.to.slice("/teams/$team".length) === (sub === "/" ? "" : sub),
    );
    if (page) crumbs.push({ label: page.label });
    return crumbs;
  }

  // Not an admin: the admin pages show the no-access page under Home.
  if (isAdminRoute(routeId) && !canAdmin)
    return [{ label: terms.home, icon: icon(Home), render: <Link to="/" /> }];
  if (isAdminRoute(routeId)) {
    const crumbs: BreadcrumbItem[] = [
      { label: "Admin", icon: icon(Shield), render: <Link to="/admin" /> },
    ];
    if (userId) {
      crumbs.push(
        { label: "Users", render: <Link to="/admin/users" /> },
        ...(isNotFound(user.error)
          ? []
          : [{ label: user.data?.user.displayName ?? "User" }]),
      );
    } else if (adminTeamSlug) {
      crumbs.push(
        { label: "Teams", render: <Link to="/admin/teams" /> },
        ...(isNotFound(adminTeam.error)
          ? []
          : [{ label: adminTeam.data?.team.name ?? adminTeamSlug }]),
      );
    } else {
      // Only the admin layout matched (an unknown admin address): the not-found page adds its own tail.
      const page = adminNav.find(
        (n) => routeId === "/app" + n.to || routeId === "/app" + n.to + "/",
      );
      if (page || routeId !== "/app/admin")
        crumbs.push({ label: page?.label ?? "Administration" });
    }
    return crumbs;
  }

  // An unknown address: NotFoundState adds "Page not found" as the tail.
  return [{ label: terms.home, icon: icon(Home), render: <Link to="/" /> }];
}

/** "Team settings · Platform Tools · <instance>": the last two crumbs, most specific first. */
export function documentTitle(crumbs: BreadcrumbItem[], instanceName: string) {
  const all = crumbs
    .filter((c) => !c.collapsed)
    .map((c) => (typeof c.label === "string" ? c.label : ""))
    .filter(Boolean);
  // "Home" names a page only on its own: under another page ("Page not found") it's the way back, not a place.
  const labels = all.length > 1 ? all.filter((l) => l !== terms.home) : all;
  return [...labels.slice(-2).reverse(), instanceName].join(" · ");
}

/**
 * The trail as shown: on a phone (below 600px), a trail of more than two
 * crumbs keeps the first and the last and collapses the ones between into
 * "…", a menu of them in order, so every trail stays on one line. Wider,
 * every crumb shows. The document title and the back links read the full trail.
 */
export function fitCrumbs(
  trail: BreadcrumbItem[],
  narrow: boolean,
): BreadcrumbItem[] {
  // A crumb without text (a tab whose name is still loading) never shows as an empty step.
  const crumbs = trail.some((c) => c.label === "")
    ? trail.filter((c) => c.label !== "")
    : trail;
  if (!narrow || crumbs.length <= 2) return crumbs;
  const middle = crumbs.slice(1, -1);
  const labels = middle
    .map((c) => (typeof c.label === "string" ? c.label : ""))
    .filter(Boolean);
  return [
    crumbs[0]!,
    {
      label: labels.length ? labels.join(", ") : "More pages",
      collapsed: middle,
    },
    crumbs[crumbs.length - 1]!,
  ];
}
