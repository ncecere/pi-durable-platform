/*
 * The model catalog: providers and their models, in two scopes. Admin: the
 * platform's providers and every team's (/v1/admin/...). Team: a team's own
 * providers (/v1/teams/{team}/model-providers...), proxy-only and for that
 * team alone. Hooks take the scope, and query keys include it:
 *
 *   ["models", "admin", …]          platform admins' lists and records
 *   ["models", "team", team, …]     a team's own providers, and the models it may use
 */
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, ifMatch, type Schemas, unwrap } from "./client";

export type ModelProvider = Schemas["ModelProvider"];
export type Model = Schemas["Model"];
export type ModelOwner = Schemas["ModelOwner"];
export type ProviderType = Schemas["ProviderType"];
export type ProviderKind = ProviderType["id"];
export type DiscoveredModel = Schemas["DiscoveredModel"];
export type AgentModel = Schemas["AgentModel"];
export type ModelSettings = Schemas["ModelSettings"];

/** Whose providers: the platform admin's routes, or one team's own. */
export type ModelScope = { kind: "admin" } | { kind: "team"; team: string };
export const adminScope: ModelScope = { kind: "admin" };
export const teamScope = (team: string): ModelScope => ({ kind: "team", team });

export const modelsKey = ["models"];
const scopeKey = (scope: ModelScope) => (scope.kind === "admin" ? [...modelsKey, "admin"] : [...modelsKey, "team", scope.team]);

/** ?owner= of the admin lists: every owner ("all"), the platform's, or one team's slug. */
export type OwnerFilter = string;

/** The provider types the scope may add, with their default base URLs. */
export const providerKindsQuery = (scope: ModelScope) =>
  queryOptions({
    queryKey: [...scopeKey(scope), "kinds"],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/model-provider-kinds"))
        : unwrap(await api.GET("/v1/teams/{team}/model-provider-kinds", { params: { path: { team: scope.team } } })),
    staleTime: 60 * 60_000,
  });

/** Admin: every owner's providers (or one owner's); team: the team's own. */
export const providersQuery = (scope: ModelScope, owner: OwnerFilter = "all") =>
  queryOptions({
    queryKey: [...scopeKey(scope), "providers", "list", scope.kind === "admin" ? owner : "own"],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/model-providers", { params: { query: owner === "all" ? {} : { owner } } }))
        : unwrap(await api.GET("/v1/teams/{team}/model-providers", { params: { path: { team: scope.team } } })),
  });

export const providerQuery = (scope: ModelScope, providerId: string) =>
  queryOptions({
    queryKey: [...scopeKey(scope), "providers", providerId],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/model-providers/{providerId}", { params: { path: { providerId } } }))
        : unwrap(await api.GET("/v1/teams/{team}/model-providers/{providerId}", { params: { path: { team: scope.team, providerId } } })),
  });

export const providerModelsQuery = (scope: ModelScope, providerId: string) =>
  queryOptions({
    queryKey: [...scopeKey(scope), "providers", providerId, "models"],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.GET("/v1/admin/model-providers/{providerId}/models", { params: { path: { providerId } } }))
        : unwrap(await api.GET("/v1/teams/{team}/model-providers/{providerId}/models", { params: { path: { team: scope.team, providerId } } })),
  });

/** What the provider offers now (a POST, but it changes nothing); `added` marks the ones in the catalog. */
export const discoverQuery = (scope: ModelScope, providerId: string) =>
  queryOptions({
    queryKey: [...scopeKey(scope), "providers", providerId, "discover"],
    queryFn: async () =>
      scope.kind === "admin"
        ? unwrap(await api.POST("/v1/admin/model-providers/{providerId}/discover", { params: { path: { providerId } } }))
        : unwrap(await api.POST("/v1/teams/{team}/model-providers/{providerId}/discover", { params: { path: { team: scope.team, providerId } } })),
    gcTime: 0,
    refetchOnWindowFocus: false,
  });

/** Every model of every provider (admin), or one owner's ("platform", a team's slug). */
export const adminModelsQuery = (owner: OwnerFilter = "all") =>
  queryOptions({
    queryKey: [...modelsKey, "admin", "all", owner],
    queryFn: async () => unwrap(await api.GET("/v1/admin/models", { params: { query: owner === "all" ? {} : { owner } } })),
  });

/** The models a team may use: its own providers' and the platform's published to it, enabled. */
export const teamModelsQuery = (team: string) =>
  queryOptions({
    queryKey: [...modelsKey, "team", team, "usable"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/models", { params: { path: { team } } })),
  });

/** Active teams to publish a model to (the first 200, by name). */
export const teamChoicesQuery = () =>
  queryOptions({
    queryKey: ["admin", "teams", "choices"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams", { params: { query: { status: "active", limit: 200 } } })).items,
    staleTime: 60_000,
  });

/** Whether teams may add providers, and the private networks they may be on (platform admins and auditors). */
export const modelSettingsQuery = () =>
  queryOptions({
    queryKey: ["admin", "settings", "models"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/settings/models")),
  });

/*
 * After a provider or model changed. A team's change shows in its own lists
 * and the admin's (which list every owner's); an admin's change may reach any
 * team (a platform model published to it, or a team's provider), so everything.
 */
export async function invalidateModels(qc: QueryClient, scope: ModelScope = adminScope): Promise<void> {
  if (scope.kind === "admin") return qc.invalidateQueries({ queryKey: modelsKey });
  await Promise.all([qc.invalidateQueries({ queryKey: scopeKey(scope) }), qc.invalidateQueries({ queryKey: scopeKey(adminScope) })]);
}

/* ---------------- changes ---------------- */

export async function createProvider(scope: ModelScope, body: Schemas["ModelProviderCreate"]) {
  if (scope.kind === "admin") return unwrap(await api.POST("/v1/admin/model-providers", { body }));
  return unwrap(await api.POST("/v1/teams/{team}/model-providers", { params: { path: { team: scope.team } }, body }));
}

export async function updateProvider(scope: ModelScope, p: Pick<ModelProvider, "id" | "revision">, body: Schemas["ModelProviderUpdate"]) {
  const header = ifMatch(p.revision);
  if (scope.kind === "admin") return unwrap(await api.PATCH("/v1/admin/model-providers/{providerId}", { params: { path: { providerId: p.id }, header }, body }));
  return unwrap(await api.PATCH("/v1/teams/{team}/model-providers/{providerId}", { params: { path: { team: scope.team, providerId: p.id }, header }, body }));
}

export async function deleteProvider(scope: ModelScope, providerId: string) {
  if (scope.kind === "admin") return unwrap(await api.DELETE("/v1/admin/model-providers/{providerId}", { params: { path: { providerId } } }));
  return unwrap(await api.DELETE("/v1/teams/{team}/model-providers/{providerId}", { params: { path: { team: scope.team, providerId } } }));
}

export async function createModel(scope: ModelScope, providerId: string, body: Schemas["ModelCreate"]) {
  if (scope.kind === "admin") return unwrap(await api.POST("/v1/admin/model-providers/{providerId}/models", { params: { path: { providerId } }, body }));
  return unwrap(await api.POST("/v1/teams/{team}/model-providers/{providerId}/models", { params: { path: { team: scope.team, providerId } }, body }));
}

export async function updateModel(scope: ModelScope, m: Pick<Model, "id" | "revision">, body: Schemas["ModelUpdate"]) {
  const header = ifMatch(m.revision);
  if (scope.kind === "admin") return unwrap(await api.PATCH("/v1/admin/models/{modelId}", { params: { path: { modelId: m.id }, header }, body }));
  return unwrap(await api.PATCH("/v1/teams/{team}/models/{modelId}", { params: { path: { team: scope.team, modelId: m.id }, header }, body }));
}

export async function deleteModel(scope: ModelScope, modelId: string) {
  if (scope.kind === "admin") return unwrap(await api.DELETE("/v1/admin/models/{modelId}", { params: { path: { modelId } } }));
  return unwrap(await api.DELETE("/v1/teams/{team}/models/{modelId}", { params: { path: { team: scope.team, modelId } } }));
}
