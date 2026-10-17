# Pi Durable Platform — design

Status: **development spike**. Decisions here were made with the product owner on 2026-10-17; items marked
*proposed* are defaults to revisit.

## Goal

An enterprise-style, self-hosted platform where teams deploy, test and manage
[Pi Durable](https://github.com/earendil-works/pi/tree/main/packages/durable) agents, each in its own isolated runtime
on Kubernetes. It is the **console and API**, not the end-user product: people use agents through the teams' own apps.

- A chat application (for example `pi-d-ui`) gives **every person who signs in to it their own assistant**: its
  service account asks the platform for one agent per user (`PUT /v1/teams/{team}/agents/by-key/{userId}`) and relays
  their chats through the platform API.
- A team deploys an agent from a template, tests it in the console, and reaches it from its own integration.

Later: teams build and upload their own agents (self-service). That requires stronger isolation first (below).

## Decisions

| # | Decision | Why |
|---|---|---|
| 1 | Go control plane in the style of Grounded / Open MCP Portal: OIDC + dev personas, fixed platform and team roles, Postgres (pgx, sqlc, goose), OpenAPI, embedded React UI. | Proven patterns, one binary, same RBAC model. |
| 2 | Pi Durable runs in a separate **Node runtime** (`runtime/`), one process per agent. | Pi Durable is TypeScript. |
| 3 | **Agent state in PostgreSQL**, not SQLite. Pi's own `SqliteStorage` runs unchanged on a small SQL facade over one dedicated pg connection (`runtime/src/storage`). | Stateless agent pods: an agent can wake on any node, no node-pinned volumes. |
| 4 | **One schema and one database role per agent**, in a database separate from platform metadata. | Database-enforced isolation between agents; easy delete. |
| 5 | **Single owner per agent** via a session advisory lock on the agent's only connection, never re-established. | A stale pod that loses its connection loses its ability to write at the same moment its lock is released. |
| 6 | **One runtime per active agent; idle agents hibernate** (no pod, no connection). Requests wake them. | Thousands of per-user agents cannot all hold pods. |
| 7 | Clients reach agents **only through the platform API**, which authenticates, authorizes and proxies. Runtimes require a per-agent bearer token. | One enforcement point; runtimes are never exposed. |
| 11 | **Agents belong to teams.** The console is for service owners and platform admins; end users use the teams' apps. | Ownership, data and cost follow the team. |
| 12 | **Service-managed agents** (created by a service account with its own key) hold the app's users' conversations: only the team's service accounts use them; owners and admins see and delete them, never read them. | End users' chats aren't readable by every team member. A future support-access feature must be explicit and audited. |
| 13 | **Templates are the governance unit** (built): platform-curated catalog templates and team templates, versioned, agents pinned to a version, team templates published to the catalog with platform-admin approval. Org-wide agents are per-person instances of a template, not one shared agent. | One agent shares one store: shared agents would mix teams' data. |
| 8 | Agents come from **admin-approved templates** first. Self-service upload is later and gated on sandboxed runtimes (gVisor/Kata). | Template code is trusted; uploaded code is not. |
| 9 | Files and artifacts go to **object storage** with per-agent prefixes; working filesystems are ephemeral unless a template needs one. *(proposed, not built)* | Object storage isolates by prefix; NFS is unsuitable for workspaces. |
| 10 | Model credentials are injected into runtimes by the platform; browsers never see them. | |

## Architecture

```text
Browser / chat app / service ──► Platform (Go)
                                   ├─ auth, teams, roles, service accounts, audit
                                   ├─ templates, agents (owner: a person or a team)
                                   ├─ provisioner: per-agent schema + role in the agents database
                                   ├─ launcher: start / stop agent runtimes (local processes | Kubernetes pods | Agent Sandbox)
                                   ├─ reaper: hibernate idle agents
                                   └─ proxy: /v1/agents/{id}/threads/... → the agent's runtime (SSE streaming)
                                              │ bearer token
                                              ▼
                                   Agent runtime (Node, Pi Durable) ── its schema in PostgreSQL
```

## Launchers

| `AGENTS_LAUNCHER` | Runtime per active agent | Hibernate | Wake |
| --- | --- | --- | --- |
| `local` | a child process of the platform (development only) | stop the process | start it |
| `kubernetes` | a pod `agent-<id>`, secrets in a Secret of the same name | delete the pod | create it |
| `agent-sandbox` | a [Sandbox](https://agent-sandbox.sigs.k8s.io) `agent-<id>` around the same pod | `operatingMode: Suspended` | `Running` |
| `agent-sandbox` + `AGENTS_SANDBOX_WARM_POOL` | a standby runtime claimed from a `SandboxWarmPool` (claim `agent-<id>`) | release the claim | claim + activate |

Warm mode: the pool's pods run the runtime image without `AGENT_ID`, with `RUNTIME_ACTIVATION_TOKEN`; they are Ready
and wait (`runtime/src/standby.ts`). Waking an agent creates its claim, and the platform `POST`s the agent's
configuration (schema, database URL, runtime token, model settings) to the claimed runtime's `/activate` with the
activation token. From then on the runtime is that agent and only accepts the agent's own token. Nothing about the
agent is stored in Kubernetes, and the pool refills after each claim, so a wake skips scheduling, image pull and Node
start-up. While the previous runtime is still releasing the agent, activation answers `409 owned_elsewhere` and the
platform retries. A runtime that restarts comes back in standby and is activated again on the next request.

The activation token is in a Secret in the agents namespace (`AGENTS_SANDBOX_ACTIVATION_SECRET`; the platform creates
it with a random token if missing, and the pool's pods read it), or given to the platform directly
(`AGENTS_SANDBOX_ACTIVATION_TOKEN`). It reaches only the platform and the pool's pods; agent pods take traffic from the
platform alone, so one agent cannot activate another runtime. To rotate it, delete the Secret and the pool's unclaimed
sandboxes.

## Verified so far

- `runtime/test/conformance.test.ts`: pi-durable 1.3.0's own storage conformance suite (24 cases) passes on PostgreSQL.
- Ownership: a second opener is refused; an owner whose backend is terminated can no longer read or write, and the
  next owner opens (`ownership.test.ts`).
- Isolation: an agent role cannot read another agent's schema (`ownership.test.ts`).
- A real Harness keeps its conversation and deduplicates retried requests across a process restart
  (`harness.test.ts`); a manual `kill -9` mid-reply resumed and completed the reply.

## Known limits and open items

- Database roles are server-wide: agent roles outlive a dropped agents database (tests remove theirs);
  `prune-agent-storage` only considers schemas in its own database.
- **Advisory locks are database-wide**: any role in the agents database can take another agent's lock key and block
  it from starting. Acceptable for trusted templates; uploaded agents must not get direct database credentials (a
  storage proxy instead).
- Schema-per-agent means ~25 relations per agent. Fine for thousands; for much larger counts, a native storage with an
  `agent_id` column and row-level security may be needed.
- Commit latency is a network round trip; Pi's `settings.progress` intervals can be raised if needed.
- pi-durable is experimental and pinned to 1.3.0; the SQL facade rejects SQLite constructs it does not translate, and
  the conformance suite guards every upgrade.
- Scheduled work while an agent hibernates needs the platform to know the next wake time (not built yet).
- Hostile-code isolation (gVisor/Kata on Talos) is required before self-service uploads.
