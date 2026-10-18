/* The models a template may run on: its team's (published to it, enabled), or for a platform template every enabled model. */
import { useQuery } from "@tanstack/react-query";
import { adminModelsQuery, type Model, teamModelsQuery } from "../../api/models";

export function useTemplateModels(team: string | null | undefined, enabled = true): { models: Model[] | undefined; loading: boolean } {
  const teamModels = useQuery({ ...teamModelsQuery(team ?? ""), enabled: enabled && !!team });
  const all = useQuery({ ...adminModelsQuery(), enabled: enabled && !team });
  if (team) return { models: teamModels.data, loading: teamModels.isLoading };
  return { models: all.data?.filter((m) => m.enabled && m.providerEnabled !== false), loading: all.isLoading };
}
