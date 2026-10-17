/* Agent templates: the catalog, team templates, one template and its versions. */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type AgentTemplate = Schemas["AgentTemplate"];
export type AgentTemplateVersion = Schemas["AgentTemplateVersion"];

export const templatesKey = ["templates"];

/** What a team can deploy: the catalog, and with `team` that team's own templates. */
export const catalogQuery = (team?: string) =>
  queryOptions({
    queryKey: [...templatesKey, "catalog", team ?? ""],
    queryFn: async () => unwrap(await api.GET("/v1/agent-templates", { params: { query: team ? { team } : {} } })),
  });

export const teamTemplatesQuery = (team: string) =>
  queryOptions({
    queryKey: [...templatesKey, "team", team],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/agent-templates", { params: { path: { team } } })),
  });

export const adminTemplatesQuery = (status?: "pending") =>
  queryOptions({
    queryKey: [...templatesKey, "admin", status ?? ""],
    queryFn: async () => unwrap(await api.GET("/v1/admin/agent-templates", { params: { query: status ? { status } : {} } })),
  });

export const templateQuery = (templateId: string) =>
  queryOptions({
    queryKey: [...templatesKey, templateId],
    queryFn: async () => unwrap(await api.GET("/v1/agent-templates/{templateId}", { params: { path: { templateId } } })),
  });

export const templateVersionsQuery = (templateId: string) =>
  queryOptions({
    queryKey: [...templatesKey, templateId, "versions"],
    queryFn: async () => unwrap(await api.GET("/v1/agent-templates/{templateId}/versions", { params: { path: { templateId } } })),
  });
