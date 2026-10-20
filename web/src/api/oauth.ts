/*
 * OAuth Connect (api/openapi.yaml, tag oauth): the consent page's view of an
 * app's authorization request, the apps a person connected (theirs, or
 * anyone's for platform staff), the trusted apps that skip the consent
 * page and their app secrets, and the apps people connected. See DESIGN.md ›
 * OAuth Connect.
 */
import { queryOptions } from "@tanstack/react-query";
import { api, type Schemas, unwrap } from "./client";

export type OAuthClientInfo = Schemas["OAuthClientInfo"];
export type OAuthConsent = Schemas["OAuthConsent"];
export type OAuthDecision = Schemas["OAuthConsentDecision"]["decision"];
export type OAuthGrant = Schemas["OAuthGrant"];
export type TrustedClient = Schemas["TrustedClient"];
export type TrustedClients = Schemas["TrustedClients"];
export type AppSecret = Schemas["AppSecret"];
export type AppSecretCreated = Schemas["AppSecretCreated"];
export type ConnectedApp = Schemas["ConnectedApp"];

/**
 * The consent page's view of an authorization request. The query goes to the
 * server exactly as the browser received it (a repeated parameter is the
 * server's to refuse, not the page's to drop).
 */
export const oauthConsentQuery = (rawQuery: string) =>
  queryOptions({
    queryKey: ["oauth", "consent", rawQuery],
    queryFn: async () => unwrap(await api.GET("/v1/oauth/consent", { params: { query: {} }, querySerializer: () => rawQuery })),
    retry: false,
    staleTime: Infinity,
  });

/** Whose connected apps: the signed-in person's, or a person's on their admin page. */
export type GrantsOwner = { self: true } | { self: false; userId: string; name: string };

export const grantsKey = (owner: GrantsOwner) => (owner.self ? ["my", "oauth-grants"] : ["admin", "user", owner.userId, "oauth-grants"]);

export const grantsQuery = (owner: GrantsOwner) =>
  queryOptions({
    queryKey: grantsKey(owner),
    queryFn: async () =>
      owner.self
        ? unwrap(await api.GET("/v1/me/oauth-grants"))
        : unwrap(await api.GET("/v1/admin/users/{userId}/oauth-grants", { params: { path: { userId: owner.userId } } })),
  });

/** Disconnects one connected app (its tokens stop at once). */
export async function revokeGrant(owner: GrantsOwner, grantId: string) {
  return owner.self
    ? unwrap(await api.DELETE("/v1/me/oauth-grants/{grantId}", { params: { path: { grantId } } }))
    : unwrap(await api.DELETE("/v1/admin/users/{userId}/oauth-grants/{grantId}", { params: { path: { userId: owner.userId, grantId } } }));
}

/** The trusted apps (platform admins and auditors). */
export const trustedClientsQuery = () =>
  queryOptions({
    queryKey: ["admin", "settings", "oauth-clients"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/settings/oauth-clients")),
  });

/** Trusted apps' secrets, never the secrets themselves (platform admins and auditors). */
export const appSecretsQuery = () =>
  queryOptions({
    queryKey: ["admin", "settings", "oauth-clients", "secrets"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/settings/oauth-clients/secrets")),
  });

/** The apps people have connected, most people first (platform admins and auditors). */
export const connectedAppsQuery = () =>
  queryOptions({
    queryKey: ["admin", "oauth-clients"],
    queryFn: async () => unwrap(await api.GET("/v1/admin/oauth-clients")),
  });
