/*
 * The tabs of each page with sections, in order (the first is the default).
 * Kept here so the router can validate ?tab= without loading page chunks.
 */

/** Admin → Teams › one team: Overview · Members · Settings; a personal workspace: Overview · Limits · Settings. */
export const adminTeamTabs = ["overview", "members", "limits", "agents", "settings"] as const;
export type AdminTeamTab = (typeof adminTeamTabs)[number];

/** Admin → Models and Team → Models: Providers · Models. */
export const modelCatalogTabs = ["providers", "models"] as const;
export type ModelCatalogTab = (typeof modelCatalogTabs)[number];

/** Admin → Models › one provider (and a team's own): Models · Settings. */
export const providerTabs = ["models", "settings"] as const;
export type ProviderTab = (typeof providerTabs)[number];

/** Team → MCP servers: Servers (the team's own) · Available (every one it may use). */
export const mcpCatalogTabs = ["servers", "available"] as const;
export type McpCatalogTab = (typeof mcpCatalogTabs)[number];

/** Admin → MCP servers › one server (and a team's own): Tools · Settings. */
export const mcpServerTabs = ["tools", "settings"] as const;
export type McpServerTab = (typeof mcpServerTabs)[number];

/** A team's service account: Overview (role and access) · Keys · Settings. */
export const serviceAccountTabs = ["overview", "keys", "settings"] as const;
export type ServiceAccountTab = (typeof serviceAccountTabs)[number];

/** A team's agent: Overview (status, how to call it) · Test (chat with it) · Conversations (platform staff) · Settings. */
export const agentTabs = ["overview", "test", "conversations", "settings"] as const;
export type AgentTab = (typeof agentTabs)[number];

/** A template: Overview (current instructions) · Versions · Settings. */
export const templateTabs = ["overview", "versions", "settings"] as const;
export type TemplateTab = (typeof templateTabs)[number];

/** Team settings: Members (with open invites) · Audit log · General. */
export const teamSettingsTabs = ["members", "audit", "general"] as const;
export type TeamSettingsTab = (typeof teamSettingsTabs)[number];

/**
 * validateSearch for a route with tabs: keeps ?tab= when it names a tab other
 * than the first. With `passthrough`, other parameters (list filters, ?record=,
 * date ranges) are kept too.
 */
export function tabSearch<T extends string>(tabs: readonly T[], opts: { passthrough?: boolean } = {}) {
  return (s: Record<string, unknown>): { tab?: T } => {
    const tab = tabs.includes(s.tab as T) && s.tab !== tabs[0] ? { tab: s.tab as T } : {};
    if (!opts.passthrough) return tab;
    const { tab: _drop, ...rest } = s;
    return { ...rest, ...tab };
  };
}
