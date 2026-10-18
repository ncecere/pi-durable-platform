/* API fixtures for the page tests, typed from the generated contract (schema.gen.ts), so they break when it changes. */
import type { Schemas } from "../api/client";

type PlatformRole = Schemas["PlatformRole"];
type TeamRole = Schemas["TeamRole"];

export const instance: Schemas["InstanceInfo"] = { name: "Pi Durable Platform", orgName: "", theme: "neutral", logoUrl: null, supportUrl: null };

export const authConfig = (extra: Partial<Schemas["AuthConfig"]> = {}): Schemas["AuthConfig"] => ({
  instance,
  oidcEnabled: false,
  devAuthEnabled: true,
  loginUrl: "/auth/login",
  teamRequestUrl: null,
  devAccounts: [
    { id: "admin", name: "Ada Admin", email: "admin@example.com", platformRole: "platform_admin" },
    { id: "auditor", name: "Avery Auditor", email: "auditor@example.com", platformRole: "platform_auditor" },
    { id: "alex", name: "Alex Owner", email: "alex@example.com", platformRole: "none" },
  ],
  ...extra,
});

export const team: Schemas["Team"] = {
  id: "11111111-1111-4111-8111-111111111111",
  slug: "platform-tools",
  name: "Platform Tools",
  description: "Internal agents for developer tooling.",
  status: "active",
  revision: 3,
  createdAt: "2026-09-01T10:00:00Z",
  updatedAt: "2026-09-01T10:00:00Z",
  archivedAt: null,
};

export const myTeam = (role: TeamRole = "owner", extra: Partial<Schemas["MyTeam"]> = {}): Schemas["MyTeam"] => ({
  id: team.id,
  slug: team.slug,
  name: team.name,
  status: "active",
  role,
  ...extra,
});

export const user = (extra: Partial<Schemas["User"]> = {}): Schemas["User"] => ({
  id: "22222222-2222-4222-8222-222222222222",
  email: "una@example.com",
  displayName: "Una User",
  platformRole: "none",
  status: "active",
  revision: 1,
  createdAt: "2026-09-01T09:00:00Z",
  lastLoginAt: "2026-10-15T09:00:00Z",
  ...extra,
});

/** The signed-in user: a platform role and their teams (one team with `teamRole`, or none with null). */
export const meFor = (platformRole: PlatformRole = "none", teamRole: TeamRole | null = "owner"): Schemas["Me"] => ({
  user: user({ platformRole }),
  teams: teamRole ? [myTeam(teamRole)] : [],
  groups: ["everyone", "engineering"],
  csrfToken: "csrf-123",
  capabilities: { platformAdmin: platformRole === "platform_admin", platformAuditor: platformRole === "platform_auditor" },
});

export const member = (role: TeamRole, extra: Partial<Schemas["MemberUser"]> = {}, revision = 1): Schemas["Member"] => ({
  user: { id: user().id, email: user().email, displayName: user().displayName, status: "active", ...extra },
  role,
  revision,
  createdAt: "2026-09-02T10:00:00Z",
});

export const members: Schemas["Member"][] = [
  member("owner"),
  member("admin", { id: "33333333-3333-4333-8333-333333333333", email: "blair@example.com", displayName: "Blair Admin" }),
  member("editor", { id: "44444444-4444-4444-8444-444444444444", email: "casey@example.com", displayName: "Casey Editor" }),
  member("member", { id: "55555555-5555-4555-8555-555555555555", email: "drew@example.com", displayName: "Drew Member" }),
];

export const invite = (extra: Partial<Schemas["Invite"]> = {}): Schemas["Invite"] => ({
  id: "66666666-6666-4666-8666-666666666666",
  email: "new.person@example.com",
  role: "editor",
  invitedBy: user().id,
  createdAt: "2026-10-10T10:00:00Z",
  expiresAt: "2026-11-09T10:00:00Z",
  ...extra,
});

export const teamSummary = (extra: Partial<Schemas["TeamSummary"]> = {}): Schemas["TeamSummary"] => ({
  team,
  memberCount: 4,
  ownerCount: 1,
  ownerInvites: 0,
  ...extra,
});

export const auditEntry = (extra: Partial<Schemas["AuditEntry"]> = {}): Schemas["AuditEntry"] => ({
  id: 101,
  occurredAt: "2026-10-15T12:00:00Z",
  actorKind: "user",
  actorUserId: user().id,
  teamId: team.id,
  teamName: team.name,
  teamSlug: team.slug,
  action: "team.member_role_change",
  actor: { kind: "user", userId: user().id, displayName: "Una User", email: "una@example.com" },
  targetType: "user",
  targetId: "44444444-4444-4444-8444-444444444444",
  targetLabel: "Casey Editor",
  targetExists: true,
  before: { role: "member" },
  after: { role: "editor" },
  metadata: {},
  requestId: "req-1",
  via: { kind: "session" },
  clientIp: null,
  ...extra,
});

export const page = <T>(items: T[], nextCursor: string | null = null) => ({ items, nextCursor });

export const apiKey = (extra: Partial<Schemas["ApiKey"]> = {}): Schemas["ApiKey"] => ({
  id: "13131313-1313-4131-8131-131313131313",
  name: "Deploy script on my laptop",
  prefix: "pdp_ab12cd34",
  status: "active",
  createdAt: "2026-10-01T10:00:00Z",
  expiresAt: "2026-12-30T10:00:00Z",
  lastUsedAt: "2026-10-15T10:00:00Z",
  revokedAt: null,
  ...extra,
});

export const serviceAccountId = "15151515-1515-4151-8151-151515151515";

export const serviceAccount = (extra: Partial<Schemas["ServiceAccount"]> = {}): Schemas["ServiceAccount"] => ({
  id: serviceAccountId,
  teamId: team.id,
  teamSlug: team.slug,
  name: "ci-release-bot",
  description: "Runs the release workflow's smoke checks.",
  role: "member",
  enabled: true,
  activeKeyCount: 1,
  lastUsedAt: "2026-10-16T06:00:00Z",
  createdByName: "Una User",
  revision: 5,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-12T10:00:00Z",
  ...extra,
});

export const serviceAccountKey = (extra: Partial<Schemas["ServiceAccountKey"]> = {}): Schemas["ServiceAccountKey"] => ({
  id: "16161616-1616-4161-8161-161616161616",
  name: "GitHub Actions",
  prefix: "pdp_sa_ab12cd34",
  status: "active",
  createdAt: "2026-10-01T10:00:00Z",
  createdByName: "Una User",
  expiresAt: "2026-12-30T10:00:00Z",
  lastUsedAt: "2026-10-16T06:00:00Z",
  revokedAt: null,
  ...extra,
});

/* ---------------- models and agents ---------------- */

export const providerKinds: Schemas["ProviderType"][] = [
  { id: "openai", name: "OpenAI", api: "openai-responses", defaultBaseUrl: "https://api.openai.com/v1", authHeader: "authorization" },
  { id: "anthropic", name: "Anthropic", api: "anthropic-messages", defaultBaseUrl: "https://api.anthropic.com", authHeader: "x-api-key" },
  { id: "openai-compatible", name: "OpenAI-compatible (chat completions)", api: "openai-completions", defaultBaseUrl: null, authHeader: "authorization" },
];

export const providerId = "17171717-1717-4171-8171-171717171717";

export const modelProvider = (extra: Partial<Schemas["ModelProvider"]> = {}): Schemas["ModelProvider"] => ({
  id: providerId,
  name: "LiteLLM gateway",
  kind: "openai-compatible",
  baseUrl: "https://litellm.example.com/v1",
  accessMode: "proxy",
  enabled: true,
  hasHeaders: false,
  modelCount: 1,
  revision: 2,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
  ...extra,
});

export const model = (extra: Partial<Schemas["Model"]> = {}): Schemas["Model"] => ({
  id: "18181818-1818-4181-8181-181818181818",
  providerId,
  providerName: "LiteLLM gateway",
  providerKind: "openai-compatible",
  providerEnabled: true,
  accessMode: "proxy",
  upstreamId: "fake-one",
  name: "Fake One",
  contextWindow: 128000,
  maxTokens: 16000,
  images: false,
  reasoning: false,
  inputPrice: null,
  outputPrice: null,
  enabled: true,
  visibility: "all",
  teams: null,
  revision: 1,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
  ...extra,
});

export const agentId = "19191919-1919-4191-8191-191919191919";

export const agent = (extra: Partial<Schemas["Agent"]> = {}): Schemas["Agent"] => ({
  id: agentId,
  name: "Helper",
  description: "Answers questions about our tools.",
  teamId: team.id,
  teamSlug: team.slug,
  teamName: team.name,
  templateId: "20202020-2020-4202-8202-202020202020",
  templateSlug: "assistant",
  templateName: "Assistant",
  templateVersion: 1,
  templateCurrentVersion: 1,
  externalKey: null,
  enabled: true,
  running: false,
  model: null,
  lastUsedAt: null,
  revision: 4,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
  ...extra,
});
