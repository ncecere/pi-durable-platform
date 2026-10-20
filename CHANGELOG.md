# Changelog

Notable changes to Pi Durable Platform, one entry per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor releases may include breaking changes;
the entry says how to adapt. The design is in [DESIGN.md](DESIGN.md), and each release's notes are in
[`docs/releases/`](docs/releases/).

## [Unreleased]

## [0.2.0] - 2026-10-19

Run it anywhere, safely: deployment manifests, several replicas, metrics, key rotation, and a set of safety fixes.
Includes database migration `00017`. Read [the upgrade notes](docs/releases/v0.2.0.md#upgrading-from-v012).

### Added

- **A Helm chart and a Kustomize base** (`deploy/`) with RBAC, NetworkPolicies (a Cilium flavour too), Pod Security
  `restricted`, the Kubernetes and Agent Sandbox launchers, an Ingress, a PodDisruptionBudget and a ResourceQuota. The
  chart is the source of truth and the Kustomize base is generated from it (checked in CI with Helm 3 and 4). The chart
  is published, signed with cosign, to `oci://ghcr.io/ncecere/charts/pi-durable-platform` on release tags. CI installs it
  into a kind cluster and checks the isolation, using the images built for the commit under test (#1).
- **Several platform replicas.** `replicaCount: 2` is supported, with rolling updates that leave none missing, spreading
  over nodes and zones, and a PodDisruptionBudget. The runtime reports its open streams and its work to `/status`, and
  the idle reaper believes it, so a replica that never saw an agent can't hibernate it; one replica reaps at a time;
  starting an agent is serialised across replicas; concurrent start-up no longer races creating or hardening the agents
  database. Design: [`docs/replicas.md`](docs/replicas.md) (#2).
- **Metrics and tracing.** Prometheus `/metrics` on its own listener (`METRICS_ADDR`; on in the chart on port 9090, with
  no ingress allowed by default), covering requests, agent wakes, hibernations, model and MCP proxies, OAuth, sign-ins,
  rate limits, background loops, group re-checks and database pools. Labels are values the platform defines, never ids,
  team names or URLs. Optional OpenTelemetry traces over OTLP/HTTP (`OTEL_EXPORTER_OTLP_ENDPOINT`). A Grafana dashboard
  and example alerts in `deploy/grafana/`, and chart support for a ServiceMonitor, a scraper NetworkPolicy and a
  dashboard ConfigMap (#3).
- **`pi-durable-platform rekey`** re-seals every stored secret under the current `ENCRYPTION_KEY` (`--dry-run`,
  `--batch`), and `doctor` counts values still under the previous key. A test fails for any sealed column it doesn't
  cover (#4).
- **Platform-chosen thread IDs.** `POST /v1/agents/{id}/threads` returns a new random thread ID (`t_` and 128 random
  bits). MCP servers can be set only on a thread that exists or that the same call creates (#5).
- **IdP group re-checks.** With `OIDC_ALLOWED_GROUPS`, the platform asks the identity provider every
  `OIDC_GROUP_RECHECK_INTERVAL` (15 minutes) whether the person is still in an allowed group, using the refresh token of
  their last sign-in (sealed). When not, their sessions, API keys and connected apps stop working and Admin › People
  shows "Group access ended"; signing in again restores access. If the IdP can't answer, access lasts
  `OIDC_GROUP_RECHECK_GRACE` (24 hours). Needs the `offline_access` scope. Audit: `user.group_denied`,
  `user.group_restored` (#6).
- **Agents database hardening.** An agent's ownership is a row in its own schema, naming the owning database session,
  taken over only when that session is gone, so another agent's role can't block it with an advisory lock. New commands
  `prune-roles` and `prune-agent-storage`; `doctor` reports agent roles that belong to no agent, and warns when PUBLIC may
  connect to the platform database (where an agent's role could take the platform's advisory locks) (#7).
- **A configuration reference.** `.env.example` lists every setting, a test fails when one is missing, and the comments
  match the code (#23).
- **Vulnerability checks in CI:** `govulncheck` and the production-dependency audits of the web app and the runtime,
  weekly and when a dependency file changes.

### Changed

- **Thread MCP servers follow the thread record.** Setting servers on an unknown thread ID claims it (the caller becomes
  its creator; send `visibility` on the PUT for a private thread). Servers set on a thread nobody had started, which
  earlier releases accepted, are removed by migration `00017`. Servers set on a reserved thread don't reach another
  person's first message.
- **The idle reaper trusts the runtime.** `/status` reports `openStreams` next to `busyThreads` and `lastActivityAt`.
  An agent on a runtime image from before this release doesn't report it and is judged by its own replica's streams
  until it restarts.
- **Open event streams end when access ends.** Every 30 seconds a stream re-checks that the person is still active and
  may use the agent.
- `terminationGracePeriodSeconds` of the chart is 75 (the platform's own shutdown takes up to 65 seconds), and the chart
  adds `offline_access` to the OIDC scopes when `oidc.allowedGroups` is set.

### Fixed

- A runtime sending an oversized `/status` reply can no longer exhaust the platform's memory (the reply is read through
  a 64 KiB limit).
- Agents whose role created large objects or stray tables can now be deleted, and `prune-roles` removes such leftovers.
- An agent's database role can no longer block platform start-up by holding the agents database's prepare lock.
- A client's `traceparent` no longer forces tracing: `OTEL_TRACES_SAMPLER_ARG` decides.
- An unreadable stored IdP token (an `ENCRYPTION_KEY` change without `ENCRYPTION_KEY_PREVIOUS`) ends access after the
  grace period instead of exempting the person; a rotated refresh token is no longer lost when the platform restarts in
  the middle of a check; people in more than 200 IdP groups are no longer wrongly refused.
- Sign-ins that bring no refresh token while groups are enforced are logged, counted (`pdp_group_recheck_*`) and
  flagged by `doctor`.
- A status report from the agent's runtime that arrives after a thread was deleted no longer brings the thread back
  (as a shared record with the deleted thread's title, even for a private thread). In v0.1.x too.
- A reserved thread (created with `POST .../threads` or by setting servers on a new ID, no message yet) can be deleted;
  it returned 404 and stayed for 30 days.

### Security

- `golang.org/x/net` 0.60.0 fixes four HTTP/2 advisories (GO-2026-6603, -6611, -6612, -6617) that `govulncheck`
  reports as reachable from the platform. v0.1.2 and earlier are affected.
- See Fixed for the review's other findings. Keep agent roles out of the platform database: [the upgrade
  notes](docs/releases/v0.2.0.md#upgrading-from-v012) give the statements.

## [0.1.2] - 2026-10-19

### Fixed

- A connecting app's logo now shows on the approval screen, in Account › Connected apps and on a person's admin page.
  The platform fetches the logo itself (public addresses only, https only, no redirects, at most 256 KiB, PNG, JPEG,
  GIF or WebP checked by their bytes, never SVG), caches it for an hour and serves it from its own origin
  (`GET /v1/oauth/client-logo`), so the console's Content-Security-Policy stays closed to third-party images (#25).
- The agent page's status badge follows wake and hibernate without a reload, including a wake from another session
  or app (#27).

## [0.1.1] - 2026-10-18

### Added

- **Doctor checks the agents database.** `pi-durable-platform doctor` connects to `AGENTS_DATABASE_URL` and checks
  that its role can provision agents (CREATEROLE and CREATE on the database, on PostgreSQL 16 or later). Skipped
  without an agents database; reported as info when `AGENTS_CREATE_DATABASE` will create it.
- **Staff read any team's agents.** Platform admins and auditors see every team's agents read-only (`GET
  /v1/teams/{team}/agents`, `GET /v1/agents/{id}`), not only personal workspaces'. Admin › Teams › a team has an
  Agents tab linking to each agent's page (Overview and Settings; Conversations for platform admins; no Test).
- **The product's logo, "Wake".** The sidebar, the sign-in page and the session-unavailable page show Pi Durable
  Platform's mark when the instance has no logo of its own (`UI_LOGO_URL` still replaces it), and the favicon is the
  mark, with a `favicon.ico` (no more `/favicon.ico` 404) and an `apple-touch-icon.png`.

### Fixed

- Platform staff could hibernate a personal workspace's agent (`POST /v1/agents/{id}/hibernate`); now only the
  agent's team can (403 for staff).
- Sign-in: the "Help signing in" link uses the app's link colour.
- Home: the Agents card no longer shows an empty body strip (it links to All agents and the Catalog).
- Admin › Models and MCP servers: the Owner filter's chevron no longer overlaps its text (every small select).
- Admin › Templates: the review queue and the full list each have a heading, and the queue's table a name.
- Admin › Settings › Private networks: the example placeholder shows only while no network is listed.
- Audit log: every action the platform records has a label (agents, templates, model providers, models, MCP
  servers, OAuth apps, team provider settings), with areas in the filter and target type names.
- Platform admins' conversation list says "Title hidden" for a withheld title instead of "Private conversation":
  shared threads in personal workspaces and of service-managed agents are withheld too; Visibility says who sees it.
- Agent Test chat: the header's Shared icon stays on the line with its word.

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

[Unreleased]: https://github.com/ncecere/pi-durable-platform/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/ncecere/pi-durable-platform/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/ncecere/pi-durable-platform/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/ncecere/pi-durable-platform/releases/tag/v0.1.0
