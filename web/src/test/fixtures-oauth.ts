/* OAuth Connect fixtures: apps (metadata document and registered), the consent view, connected apps and trusted apps. */
import type { Schemas } from "../api/client";

export const oauthClient = (extra: Partial<Schemas["OAuthClientInfo"]> = {}): Schemas["OAuthClientInfo"] => ({
  id: "https://chat.example.com/oauth/client-metadata.json",
  kind: "metadata",
  name: "Team Chat",
  host: "chat.example.com",
  uri: "https://chat.example.com",
  trusted: false,
  ...extra,
});

export const registeredClient = (extra: Partial<Schemas["OAuthClientInfo"]> = {}): Schemas["OAuthClientInfo"] => ({
  id: "pdpcl_3f9a1c",
  kind: "registered",
  name: "Handy Helper",
  host: "helper.example.net",
  trusted: false,
  ...extra,
});

export const oauthConsent = (extra: Partial<Schemas["OAuthConsent"]> = {}): Schemas["OAuthConsent"] => ({
  client: oauthClient(),
  redirectUri: "https://chat.example.com/oauth/callback",
  redirectHost: "chat.example.com",
  scopes: ["agents"],
  remembered: false,
  personalWorkspace: { slug: "personal-222222222222", name: "Personal" },
  ...extra,
});

export const oauthGrant = (extra: Partial<Schemas["OAuthGrant"]> = {}): Schemas["OAuthGrant"] => ({
  id: "abababab-abab-4bab-8bab-abababababab",
  client: oauthClient({ trusted: true }),
  scopes: ["agents"],
  createdAt: "2026-10-10T10:00:00Z",
  lastUsedAt: "2026-10-17T10:00:00Z",
  ...extra,
});

export const trustedClients = (extra: Partial<Schemas["TrustedClients"]> = {}): Schemas["TrustedClients"] => ({
  clients: [
    { clientId: "https://chat.example.com/oauth/client-metadata.json", label: "Team Chat" },
    { clientId: "https://notes.example.org/oauth/client.json", label: "Notes" },
  ],
  revision: 2,
  updatedAt: "2026-10-12T10:00:00Z",
  ...extra,
});
