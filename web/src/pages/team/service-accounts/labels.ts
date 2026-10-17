/*
 * Service accounts' words, rules and queries. The rules mirror the server's
 * (api/openapi.yaml);
 * the server still decides: the team's owners and admins of an active team
 * create, change and delete service accounts and their keys; members and
 * platform staff read the accounts; the keys are listed to owners, admins
 * and platform staff.
 */
import { queryOptions, type QueryClient } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "../../../api/client";
import { serviceAccountQuery, serviceAccountsKey } from "../../../api/queries";
import { roleLabels } from "../../../components/roles";

export type ServiceAccount = Schemas["ServiceAccount"];
export type ServiceAccountKey = Schemas["ServiceAccountKey"];
export type ServiceAccountRole = Schemas["ServiceAccountRole"];

/** What a service account is, for the empty list and the create form. */
export const whatIsAServiceAccount =
  "A service account is an identity the team owns, for services, CI and scripts. Its keys don't depend on anyone staying on the team: the team's owners and admins are responsible for it.";

/** The roles it can have (never owner or admin), lowest first. */
export const serviceAccountRoles: ServiceAccountRole[] = ["member", "editor"];

export const serviceAccountRoleHelp: Record<ServiceAccountRole, string> = {
  member: "Acts with a member's access to the team's resources. Enough for most automation.",
  editor: "Also reaches what needs editors or above. Choose it only when the account needs it.",
};

/** The role's purpose in one sentence (the form and the Overview). */
export const roleExplanation =
  "The role decides what the account reaches among the team's resources. It never makes the account an owner or admin, and it can't change the team.";

export const roleLabel = (r: ServiceAccountRole) => roleLabels[r];

/** The contract's name rule: lower-case letters, digits and dashes, starting and ending with a letter or digit. */
export const namePattern = /^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$/;

export function nameProblem(name: string): string | undefined {
  if (!name) return "Enter a name.";
  return namePattern.test(name) ? undefined : "Use 1 to 64 lower-case letters, digits and dashes, starting and ending with a letter or digit.";
}

/** "CI Release Bot" → "ci-release-bot". */
export const suggestName = (text: string) =>
  text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 64)
    .replace(/-+$/, "");

/** At most two active keys, so a key can be rotated without downtime. */
export const maxActiveKeys = 2;

/** How rotation works, said the same way on the Keys tab and after creating a key. */
export const rotationSteps = "To rotate a key without downtime: create a new key, deploy it where the old one is used, then revoke the old one.";

/** "1 of 2 active". */
export const activeKeysText = (n: number) => (n === 0 ? "No active keys" : `${n} of ${maxActiveKeys} active`);

export const keyStatusLabels: Record<ServiceAccountKey["status"], string> = { active: "Active", expired: "Expired", revoked: "Revoked" };
export const keyStatusTone = { active: "success", expired: "neutral", revoked: "danger" } as const;

export type ServiceAccountAbilities = {
  /** Create, change, disable and delete it, and create and revoke its keys. */
  canManage: boolean;
  /** List its keys. */
  canReadKeys: boolean;
};

export function serviceAccountAbilities(t: { isManager: boolean; archived: boolean; staff: boolean }): ServiceAccountAbilities {
  return { canManage: t.isManager && !t.archived, canReadKeys: t.isManager || t.staff };
}

export const keysQuery = (team: string, serviceAccountId: string) =>
  queryOptions({
    queryKey: [...serviceAccountsKey(team), serviceAccountId, "keys"],
    queryFn: async () => unwrap(await api.GET("/v1/teams/{team}/service-accounts/{serviceAccountId}/keys", { params: { path: { team, serviceAccountId } } })),
  });

/** A changed account from a mutation's reply: its record shows it at once, the list and its keys load again. */
export function storeServiceAccount(qc: QueryClient, account: ServiceAccount) {
  qc.setQueryData(serviceAccountQuery(account.teamSlug, account.id).queryKey, account);
  void qc.invalidateQueries({ queryKey: serviceAccountsKey(account.teamSlug) });
}
