/*
 * Queries shared by the app shell (breadcrumbs, the team switcher) and the
 * pages. They live here, not in the page modules, so the shell doesn't pull
 * lazily loaded pages into the main bundle.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, unwrap } from "./client";

/** The team view (team + the viewer's role). Shared by the team layout and the breadcrumbs. */
export const teamQuery = (team: string) =>
  queryOptions({
    queryKey: ["team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}", { params: { path: { team } } })),
  });

export const membersKey = (team: string) => ["team", team, "members"];

/** A team's members, owners first. */
export const membersQuery = (team: string) =>
  queryOptions({
    queryKey: membersKey(team),
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/members", { params: { path: { team } } })),
  });

/** One user with their memberships (admin). */
export const adminUserQuery = (userId: string) =>
  queryOptions({
    queryKey: ["admin", "user", userId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/users/{userId}", { params: { path: { userId } } })),
  });

/** One team summary (admin). */
export const adminTeamQuery = (team: string) =>
  queryOptions({
    queryKey: ["admin", "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams/{team}", { params: { path: { team } } })),
  });

/* ---------------- service accounts and API keys ---------------- */

/** The query key prefix of a team's service accounts (the list, records and their keys). */
export const serviceAccountsKey = (team: string) => ["team", team, "service-accounts"];

/** A team's service accounts (members and platform staff). */
export const serviceAccountsQuery = (team: string) =>
  queryOptions({
    queryKey: [...serviceAccountsKey(team), "list"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/service-accounts", { params: { path: { team } } })),
  });

/** One of a team's service accounts (the record page and its breadcrumb). */
export const serviceAccountQuery = (team: string, serviceAccountId: string) =>
  queryOptions({
    queryKey: [...serviceAccountsKey(team), serviceAccountId],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/service-accounts/{serviceAccountId}", { params: { path: { team, serviceAccountId } } })),
  });

/** The person's API keys. */
export const myApiKeysQuery = () =>
  queryOptions({
    queryKey: ["my", "api-keys"],
    queryFn: async () => unwrap(await api.GET("/v1/me/api-keys")),
  });
