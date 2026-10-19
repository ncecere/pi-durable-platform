# Pi Durable Platform

A self-hosted console and API where teams deploy, test and manage agents built with [Pi](https://github.com/earendil-works/pi)
and its [durable package](https://github.com/earendil-works/pi/tree/main/packages/durable). Each agent runs in its own
runtime, keeps its state in its own PostgreSQL schema, and hibernates when idle. People don't chat with agents here: a
team's own apps (a chat app, a support tool) reach its agents through the platform API, for example one agent per user
of the app. The console is where service owners and platform admins deploy agents from templates, test them, and see
and manage the agents their apps use.

- Website: <https://pdp.bitop.dev>
- Documentation: <https://docs.pdp.bitop.dev>
- Latest release: [v0.1.2](docs/releases/v0.1.2.md) ([changelog](CHANGELOG.md)). Development software: it has not run in
  production outside the maintainer's own installation.

Pi Durable Platform is an independent project. It runs agents built with Pi and pi-durable, and is not affiliated with or
endorsed by Earendil.

## Features

- **Teams and roles.** Platform admins and auditors; team owners, admins, editors and members. Sign-in with OIDC
  (optionally limited to email domains or IdP groups), plus development personas on loopback.
- **Personal workspaces.** One per person while turned on: catalog templates only, with limits platform admins set.
- **Templates and a reviewed catalog.** Versioned platform and team templates; agents are pinned to a version; team
  templates reach the catalog after a platform admin's review.
- **Agents with their own storage.** One PostgreSQL schema and role per agent, in a database separate from the
  platform's, with a single writer per agent. Apps create one agent per user by external key (service-managed agents,
  whose conversations team members can't read).
- **Launchers.** Local processes (development), one Kubernetes pod per agent, or [Kubernetes Agent
  Sandbox](https://agent-sandbox.sigs.k8s.io), including warm pools of standby runtimes activated on wake. Idle agents
  hibernate and wake on the next request.
- **Model catalog.** OpenAI, Anthropic and compatible providers (LiteLLM, vLLM, Ollama, OpenRouter …), model discovery,
  publishing to teams, proxy (through the platform's model gateway) or direct access, team-owned providers, prices and
  usage reports with cost estimates.
- **MCP catalog.** Platform and team remote MCP servers, per-template tool selection, a logging MCP proxy, and MCP
  servers an app supplies for one thread.
- **Threads.** Recorded by the platform (listing never wakes an agent), with status, an events stream, private and
  shared threads, message authors and metadata, and audited reads by platform admins who state a reason.
- **Access for apps.** Team service accounts with expiring keys, personal API keys, and OAuth Connect (an OAuth 2.1
  authorization server, so an app can act as the person who connects it).
- **Operations and security.** Append-only audit log, `doctor`, forward-only migrations, sealed secrets, peppered key
  digests, an address guard for team-chosen URLs, production safety checks.

[The v0.1.0 release notes](docs/releases/v0.1.0.md) list what is in the first release and its known limitations ([v0.1.1](docs/releases/v0.1.1.md) and [v0.1.2](docs/releases/v0.1.2.md) are fix releases), and
[DESIGN.md](DESIGN.md) explains how it works.

## Images

| Image | What |
|---|---|
| `ghcr.io/ncecere/pi-durable-platform` | The platform: Go 1.26 binary with the React/Vite web UI embedded (distroless, non-root) |
| `ghcr.io/ncecere/pi-durable-platform-runtime` | The agent runtime ([`runtime/`](runtime), TypeScript on Node 22): one agent per container |

Releases are tagged `vX.Y.Z` (and `latest`) for linux/amd64 and linux/arm64, signed with cosign and carry SBOM and
provenance attestations; the release notes show how to verify them. Every branch push also publishes `sha-<12>` images
(linux/amd64).

## Deploying

The platform needs PostgreSQL 17 (two databases: the platform's and the agents'), an OIDC provider, TLS, and for the
`kubernetes` and `agent-sandbox` launchers a cluster where it may manage pods and secrets in the agents' namespace.
Run `pi-durable-platform migrate` and then `serve` from the platform image, set `AGENTS_IMAGE` to the runtime image (both
pinned by digest), and check the result with `pi-durable-platform doctor`.

The repository ships a [Helm chart](deploy/helm/pi-durable-platform/README.md) and a [Kustomize base with overlays](deploy/kustomize/README.md)
(both in [`deploy/`](deploy/README.md), kept in step by CI): the platform Deployment and Services, the agents namespace
with restricted Pod Security, a Role limited to the agents namespace with exactly the verbs the launcher needs, and
the NetworkPolicies that isolate agents (an agent reaches only its PostgreSQL, the platform's gateway port and DNS; only
the platform reaches an agent). The chart is published, signed, as `oci://ghcr.io/ncecere/charts/pi-durable-platform`:

```sh
helm install pi-durable-platform oci://ghcr.io/ncecere/charts/pi-durable-platform --version <version> \
  -n pi-durable-platform -f values.yaml
```

PostgreSQL, the ingress controller and Agent Sandbox's controller are not included (`deploy/examples/cloudnative-pg` has a
CloudNativePG example), and the NetworkPolicies need a CNI that enforces them. Further guidance is on the
[documentation site](https://docs.pdp.bitop.dev).

## Quick start (development)

Requires Go 1.26+, Node 22.19+, pnpm 11 (`corepack enable`), Docker, and [sqlc](https://sqlc.dev) 1.31 for
`make generate`.

```sh
make deps-up                     # PostgreSQL 17 on 127.0.0.1:55432 (docker compose project pi-durable-platform)
(cd runtime && pnpm install)     # the agent runtime (agents run as local processes in development)
make web                         # build the web UI into web/dist (embedded by the Go binary)
make run                         # creates .env from .env.example if needed, serves http://localhost:8098
```

Open <http://localhost:8098> and choose a development persona: **Dev Platform Admin** (`admin@localhost`) is a
platform admin, **Dev Platform Auditor** (`auditor@localhost`) a platform auditor, and **Dev User**, **Alex Dev**,
**Blair Dev** and **Casey Dev** have no platform role. Development sign-in (`DEV_AUTH=true`) and the example keys in
`.env.example` are refused unless `APP_URL` is a loopback address. Never expose them.

Sign in as **Dev Platform Admin**, create a team under Admin › Teams (with yourself or **Alex Dev** as its owner), then
open the team's **Agents**, deploy one and use its **Test** tab. Agents use a scripted demo model until you add a
provider under Admin › Models, or set `AGENTS_MODEL_PROVIDER=openai-compatible` with `AGENTS_MODEL_BASE_URL`,
`AGENTS_MODEL_API_KEY` and `AGENTS_MODEL_ID` (LiteLLM, vLLM, …). Agent state goes to the `pdp_agents` database
(created on start in development).

Other targets (`make help` lists them all):

| Command | What it does |
|---|---|
| `make build` | Builds `bin/pi-durable-platform` (run `make web` first to include the UI) |
| `make test` | All Go tests; integration tests skip when Postgres is unreachable |
| `make test-integration` | `make deps-up`, then all Go tests with `-race`, failing instead of skipping without Postgres |
| `make lint` | `go vet` and the gofmt check |
| `make actionlint` | Checks the GitHub Actions workflows |
| `make generate` | Regenerates sqlc queries, Go API types and the web's TypeScript API types |
| `make web-test` | Web type-check and Vitest tests |
| `make web-dev` | Vite with hot reload on :5176, proxying the API to :8098 |
| `make doctor` | Checks the configuration, Postgres, migrations and the agents database |
| `make deploy-check` | Lints and renders the Helm chart and Kustomize overlays, and checks they agree (`deploy/scripts/validate.sh`) |
| `make docker`, `make docker-runtime` | Build the two images locally |
| `make deps-down` | Stops Postgres (keeps its data) |

[CONTRIBUTING.md](CONTRIBUTING.md) covers the tests (Go, runtime and web), generated code and the release process.

## Reaching agents from an app

Create a service account in the team (role **editor** to create agents), give it a key, and call the platform with
`Authorization: Bearer pdp_sa_…`:

```sh
# One agent per user of your app: created the first time, returned after that.
curl -X PUT $PLATFORM/v1/teams/support/agents/by-key/user-42 -H "Authorization: Bearer $KEY" -d '{"name": "Assistant for user 42"}'
# Send a message (202), then follow the conversation (server-sent events, the whole view on every change).
curl -X POST $PLATFORM/v1/agents/$AGENT/threads/conv-1/messages -H "Authorization: Bearer $KEY" -d '{"text": "Hello", "requestId": "m1"}'
curl -N $PLATFORM/v1/agents/$AGENT/threads/conv-1/events -H "Authorization: Bearer $KEY"
```

Agents made this way are service-managed: only the team's service accounts talk to them. The team's owners and admins
see them in the console (status, last use) but not their conversations, and can delete them. For the team's other
agents, personal API keys (`pdp_…`) work too, acting as their person.

To act as a person instead (their personal workspace and their teams' agents), have them connect the app with **OAuth
Connect**: the platform is an OAuth 2.1 authorization server (`/.well-known/oauth-authorization-server`, PKCE, a Client
ID Metadata Document or Dynamic Client Registration), and the app's `pdpat_…` access token acts as the person, without
their platform role, on the agent routes and a few read routes such as `GET /v1/me` and `/v1/teams`. See DESIGN.md,
"OAuth Connect".

## Commands

```
pi-durable-platform serve     HTTP API, embedded UI, model gateway and background loops
pi-durable-platform migrate   apply database migrations and exit
pi-durable-platform doctor    check the configuration, Postgres, migrations and the agents database
pi-durable-platform prune-agent-storage [--dry-run]
                              remove agent storage no agent owns
pi-durable-platform prune-roles [--dry-run]
                              drop agent database roles no agent owns (left by a dropped agents database)
pi-durable-platform rekey [--dry-run] [--batch N]
                              re-seal every stored secret under the current ENCRYPTION_KEY
pi-durable-platform version   print the version
```

Configuration comes from environment variables (optionally layered over a YAML file named by `PDP_CONFIG_FILE`).
[`.env.example`](.env.example) lists every setting with its default and a one-line explanation (a test fails when a
setting is read but missing there); `internal/config/settings.go` is the table the platform reads. To rotate
`ENCRYPTION_KEY`, keep the old key as `ENCRYPTION_KEY_PREVIOUS`, run `rekey`, and remove it once `doctor` reports
nothing under it (DESIGN.md, "Secrets and key rotation").

## Layout and conventions

| Path | What |
|---|---|
| `api/openapi.yaml` | The management API contract. Change it first, then `make generate`. A test fails if served routes and the spec differ, and the authorization matrix covers every operation. |
| `deploy/` | The Helm chart, the Kustomize base and overlays (generated from the chart), example PostgreSQL manifests, and the scripts CI runs on them (`validate.sh`, `smoke-kind.sh`) |
| `migrations/` | Forward-only goose migrations (embedded). Never edit an applied migration; add the next number. |
| `internal/store/queries/` | SQL for sqlc → `internal/store/dbgen` |
| `internal/authz` | Pure authorization rules (platform and team roles) |
| `internal/httpapi` | Route table (`server.go`), handlers, middleware, the contract and authorization-matrix tests |
| `internal/{auth,teams,platform,apikeys,serviceaccounts,audit,agents,oauth}` | The services; each authorizes and audits in the same transaction as the change |
| `internal/llm`, `internal/mcp` | The model catalog and gateway; the MCP catalog and proxy |
| `internal/netguard`, `internal/teamnet` | The address guard for URLs teams and apps choose |
| `internal/agenthost` | Agent storage provisioning, launchers (local processes, Kubernetes pods, Agent Sandbox), wake/proxy/hibernate |
| `runtime/` | The Pi Durable agent runtime and its PostgreSQL storage (`pnpm test` needs the compose Postgres) |
| `web/src/router.tsx`, `web/src/components/layout/nav.tsx` | The web routes and navigation |

Success responses are `{"data": …}`; errors are `{"error": {"code", "message"}}`. Browser writes need the session's
`X-CSRF-Token` and a same-origin `Origin`. Changes are audited in the same transaction as the change itself;
`audit_log` is append-only. Contributors and coding agents: see [CONTRIBUTING.md](CONTRIBUTING.md) and
[AGENTS.md](AGENTS.md). Security problems: [SECURITY.md](SECURITY.md).

Forked from the reusable foundation of [Open MCP Portal](https://github.com/ncecere/open-mcp-portal).

## Roadmap

What comes after v0.1.0, phase by phase (deployment and operations, apps at scale, what agents can do, budgets and
limits), is in [docs/roadmap.md](docs/roadmap.md). Each item is a GitHub issue with the `roadmap` label and a milestone.

## License

[MIT](LICENSE).
