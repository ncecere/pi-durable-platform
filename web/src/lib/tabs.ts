/*
 * The tabs of each page with sections, in order (the first is the default).
 * Kept here so the router can validate ?tab= without loading page chunks.
 */

/** Admin → Teams › one team: Overview · Members · Settings. */
export const adminTeamTabs = ["overview", "members", "settings"] as const;
export type AdminTeamTab = (typeof adminTeamTabs)[number];

/** A team's service account: Overview (role and access) · Keys · Settings. */
export const serviceAccountTabs = ["overview", "keys", "settings"] as const;
export type ServiceAccountTab = (typeof serviceAccountTabs)[number];

/** A team's agent: Overview (status, how to call it) · Test (chat with it) · Settings. */
export const agentTabs = ["overview", "test", "settings"] as const;
export type AgentTab = (typeof agentTabs)[number];

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
