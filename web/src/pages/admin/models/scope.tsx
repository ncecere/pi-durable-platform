/*
 * Where a model catalog page works: Admin › Models (the platform's providers
 * and every team's) or Team › Models (the team's own). The providers' pages,
 * dialogs and forms take a CatalogScope and link, navigate and call the API
 * through it, so both areas share them.
 *
 * A team's own provider (in the team's pages, or a team-owned one in Admin)
 * is proxy-only and its models are for its team alone: no access mode to
 * choose and no publishing (visibility, teams, personal workspaces).
 */
import { Link, useNavigate } from "@tanstack/react-router";
import type { ModelProvider, ModelScope } from "@/api/models";
import type { ProviderLink } from "./columns";

export type CatalogScope = {
  scope: ModelScope;
  /** May add, change and delete (platform admins in Admin; the team's owners and admins in the team). */
  canManage: boolean;
  /** Said to viewers who can't change it. */
  readOnlyNote: string;
};

/** A link to one of the scope's providers. */
export function providerLinkFor(scope: ModelScope): ProviderLink {
  if (scope.kind === "admin") {
    return (providerId, search = {}) => <Link to="/admin/models/providers/$providerId" params={{ providerId }} search={search} />;
  }
  const team = scope.team;
  return (providerId, search = {}) => <Link to="/teams/$team/models/providers/$providerId" params={{ team, providerId }} search={search} />;
}

/** Opens a provider's page, or goes back to the scope's list. */
export function useCatalogNavigate(scope: ModelScope) {
  const navigate = useNavigate();
  return {
    provider: (providerId: string) =>
      scope.kind === "admin"
        ? navigate({ to: "/admin/models/providers/$providerId", params: { providerId } })
        : navigate({ to: "/teams/$team/models/providers/$providerId", params: { team: scope.team, providerId } }),
    list: () =>
      scope.kind === "admin" ? navigate({ to: "/admin/models", replace: true }) : navigate({ to: "/teams/$team/models", params: { team: scope.team }, replace: true }),
  };
}

/** A team's own provider: proxy only, and its models are its team's alone. */
export const isTeamOwned = (scope: ModelScope, provider: Pick<ModelProvider, "team">) => scope.kind === "team" || Boolean(provider.team);
