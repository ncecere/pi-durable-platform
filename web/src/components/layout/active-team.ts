/*
 * The team the workspace sidebar and the team switcher show: the page's team
 * (a team page), else the team the user last worked in, else their first
 * team. The page's team is remembered when the user is a member of it.
 */
import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { teamQuery } from "../../api/queries";
import { type Me } from "../../session";
import { isTeamRoute, type Location } from "./location";

const lastTeamKey = "pdp.lastTeam";

export function readLastTeam() {
  try {
    return globalThis.localStorage?.getItem(lastTeamKey) ?? undefined;
  } catch {
    return undefined;
  }
}

function rememberTeam(slug: string) {
  try {
    globalThis.localStorage?.setItem(lastTeamKey, slug);
  } catch {
    // Storage can be unavailable (private mode).
  }
}

export type ActiveTeam = {
  slug?: string;
  name?: string;
  /** The user's membership in it (absent for platform staff viewing another team). */
  membership?: Me["teams"][number];
  /** It's the team of the current page (not the remembered one). */
  fromPage: boolean;
};

export function useActiveTeam(me: Me, loc: Location): ActiveTeam {
  const pageSlug = isTeamRoute(loc.routeId) ? loc.params.team : undefined;
  const view = useQuery({ ...teamQuery(pageSlug ?? ""), enabled: !!pageSlug });
  const pageMembership = me.teams.find((t) => t.slug === pageSlug);
  // A team page whose team doesn't exist (or can't be seen) falls back to the remembered team.
  const pageTeamOk = !!pageSlug && (!!pageMembership || !!view.data);

  useEffect(() => {
    if (pageMembership) rememberTeam(pageMembership.slug);
  }, [pageMembership]);

  if (pageTeamOk) {
    return {
      slug: pageSlug,
      name: pageMembership?.name ?? view.data?.team.name,
      membership: pageMembership,
      fromPage: true,
    };
  }
  const fallback =
    me.teams.find((t) => t.slug === readLastTeam()) ?? me.teams[0];
  return {
    slug: fallback?.slug,
    name: fallback?.name,
    membership: fallback,
    fromPage: false,
  };
}
