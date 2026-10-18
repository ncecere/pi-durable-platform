# Changelog

Notable changes to Pi Durable Platform, one entry per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor releases may include breaking changes;
the entry says how to adapt. The design is in [DESIGN.md](DESIGN.md), and each release's notes are in
[`docs/releases/`](docs/releases/).

## [Unreleased]

## [0.1.0] - 2026-10-18

The first release ([release notes](docs/releases/v0.1.0.md)). Migrations `00001` to `00016`. Two images, for
linux/amd64 and linux/arm64, signed with cosign (keyless) and carrying SBOM and provenance attestations:
`ghcr.io/ncecere/pi-durable-platform` (the platform) and `ghcr.io/ncecere/pi-durable-platform-runtime` (the agent
runtime).

### Added

- **Platform.** One Go binary with the React web UI embedded, backed by PostgreSQL 17: `serve`, `migrate`, `doctor`,
  `prune-agent-storage` and `version`. OIDC sign-in (allowed email domains and IdP groups, bootstrap admin) and local
  development personas (loopback `APP_URL` only).
- **Teams and roles.** Platform admins and auditors; team owners, admins, editors and members; invites; an
  authorization matrix test over every OpenAPI operation.
- **Personal workspaces** (migration `00012`): one per person while turned on, catalog templates only, platform-wide
  defaults and per-workspace overrides for max agents, max running and a recorded monthly budget.
- **Templates** (`00004`, `00007`): versioned platform and team templates, agents pinned to a version, team templates
  published to the catalog after a platform admin's review.
- **Agents** (`00004` to `00006`): deployed from templates by team editors and tested in the console; one PostgreSQL
  schema and role per agent in a separate agents database; single ownership by session advisory lock;
  service-managed agents by external key for apps (`00005`).
- **Agent runtime** (`runtime/`): one Pi Durable agent per process, pi-durable 1.3.0's `SqliteStorage` running on
  PostgreSQL through a SQL facade (its storage conformance suite passes), standby mode for warm pools.
- **Launchers:** local processes, Kubernetes pods, and Kubernetes Agent Sandbox (a Sandbox per agent, or claims on a
  `SandboxWarmPool` of standby runtimes activated as the agent). Idle agents hibernate and wake on the next request.
- **Model catalog** (`00008`, `00009`, `00014`): providers (OpenAI, Anthropic, and OpenAI chat completions, OpenAI
  Responses and Anthropic Messages compatible servers), model discovery, publishing to all or chosen teams and to
  personal workspaces, proxy or direct access, the model gateway, team-owned providers, prices, and usage reports
  with cost estimates.
- **MCP catalog** (`00010`, `00015`): platform and team remote MCP servers, per-version tool selection, the MCP proxy
  with a call log, and MCP servers apps supply for one thread.
- **Threads** (`00011`, `00016`): the platform's record of each thread, thread status, an events stream for every
  agent the caller may use, private and shared threads, message authors and metadata, platform admins' reasoned
  reads of a conversation (audited).
- **Service accounts and personal API keys** (`00003`): expiring service account keys, two active at a time.
- **OAuth Connect** (`00013`): an OAuth 2.1 authorization server so apps act as the person who connects them (PKCE,
  Client ID Metadata Documents, Dynamic Client Registration, consent, rotating refresh tokens, trusted apps).
- **Audit log:** append-only, in the same transaction as each change; team and platform views.
- **Security:** sealed secrets (AES-256-GCM, `ENCRYPTION_KEY`), API keys and tokens stored as peppered HMAC
  digests (`API_KEY_PEPPER`), an address guard for team-chosen URLs and client metadata documents, production
  safety checks, rate limits counted in PostgreSQL.
- **Release engineering:** the `image` workflow builds both images on native amd64 and arm64 runners, checks the
  version, the non-root user and the read-only root filesystem, scans them with Trivy, publishes a multi-arch index,
  signs it, and creates the GitHub Release with SBOMs and checksums.

[Unreleased]: https://github.com/ncecere/pi-durable-platform/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ncecere/pi-durable-platform/releases/tag/v0.1.0
