/*
 * How someone without a team gets onto one (tasks H10), in the same words on Home and in the workspace switcher: the
 * platform's team request form (TEAM_REQUEST_URL) or help link (SUPPORT_URL) when it has one, otherwise "ask a platform
 * admin".
 */
import { useAuthConfig, useInstance } from "../../session";

export type JoinTeamHelp = { text: string; link?: { href: string; label: string } };

export function joinTeamHelp(teamRequestUrl?: string | null, supportUrl?: string | null): JoinTeamHelp {
  const ask = "To join a team, ask one of its owners to add you";
  if (teamRequestUrl) return { text: `${ask}, or request a new team.`, link: { href: teamRequestUrl, label: "Request a new team" } };
  if (supportUrl) return { text: `${ask}, or ask for help getting onto one.`, link: { href: supportUrl, label: "Get help" } };
  return { text: `${ask}. Not sure who? Ask a platform admin.` };
}

export function useJoinTeamHelp(): JoinTeamHelp {
  const config = useAuthConfig();
  const instance = useInstance();
  return joinTeamHelp(config.data?.teamRequestUrl, instance.supportUrl);
}
