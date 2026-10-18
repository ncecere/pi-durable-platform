/* The model catalog: providers and their models (admin), and the models a team may use. */
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type ModelProvider = Schemas["ModelProvider"];
export type Model = Schemas["Model"];
export type ProviderType = Schemas["ProviderType"];
export type ProviderKind = ProviderType["id"];
export type DiscoveredModel = Schemas["DiscoveredModel"];
export type AgentModel = Schemas["AgentModel"];

export const modelsKey = ["models"];

/** The provider types the platform supports, with their default base URLs. */
export const providerKindsQuery = () =>
  queryOptions({
    queryKey: [...modelsKey, "kinds"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/model-provider-kinds")),
    staleTime: 60 * 60_000,
  });

export const providersQuery = () =>
  queryOptions({
    queryKey: [...modelsKey, "providers"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/model-providers")),
  });

export const providerQuery = (providerId: string) =>
  queryOptions({
    queryKey: [...modelsKey, "providers", providerId],
    queryFn: async () => unwrap(await api.GET("/v1/admin/model-providers/{providerId}", { params: { path: { providerId } } })),
  });

export const providerModelsQuery = (providerId: string) =>
  queryOptions({
    queryKey: [...modelsKey, "providers", providerId, "models"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/model-providers/{providerId}/models", { params: { path: { providerId } } })),
  });

/** What the provider offers now (a POST, but it changes nothing); `added` marks the ones in the catalog. */
export const discoverQuery = (providerId: string) =>
  queryOptions({
    queryKey: [...modelsKey, "providers", providerId, "discover"],
    queryFn: async () => unwrap(await api.POST("/v1/admin/model-providers/{providerId}/discover", { params: { path: { providerId } } })),
    gcTime: 0,
    refetchOnWindowFocus: false,
  });

/** Every model of every provider (admin). */
export const adminModelsQuery = () =>
  queryOptions({
    queryKey: [...modelsKey, "all"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/models")),
  });

/** The models a team may use: published to it and enabled. */
export const teamModelsQuery = (team: string) =>
  queryOptions({
    queryKey: [...modelsKey, "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/models", { params: { path: { team } } })),
  });

/** Active teams to publish a model to (the first 200, by name). */
export const teamChoicesQuery = () =>
  queryOptions({
    queryKey: ["admin", "teams", "choices"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/teams", { params: { query: { status: "active", limit: 200 } } })).items,
    staleTime: 60_000,
  });

/** After a provider or model changed: every model list and record. */
export function invalidateModels(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: modelsKey });
}
