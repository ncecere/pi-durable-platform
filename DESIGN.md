# Pi Durable Platform — design

Status: **development spike**. Decisions here were made with the product owner on 2026-10-17; items marked
*proposed* are defaults to revisit.

## Goal

An enterprise-style, self-hosted platform where teams and services deploy and manage
[Pi Durable](https://github.com/earendil-works/pi/tree/main/packages/durable) agents, each in its own isolated runtime
on Kubernetes. Examples:

- A chat application gives **every person who signs in their own assistant**: one durable agent per user.
- A team deploys an agent from a template and reaches it through its own integration (API, service account).

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
| 8 | Agents come from **admin-approved templates** first. Self-service upload is later and gated on sandboxed runtimes (gVisor/Kata). | Template code is trusted; uploaded code is not. |
| 9 | Files and artifacts go to **object storage** with per-agent prefixes; working filesystems are ephemeral unless a template needs one. *(proposed, not built)* | Object storage isolates by prefix; NFS is unsuitable for workspaces. |
| 10 | Model credentials are injected into runtimes by the platform; browsers never see them. | |

## Architecture

```text
Browser / chat app / service ──► Platform (Go)
                                   ├─ auth, teams, roles, service accounts, audit
                                   ├─ templates, agents (owner: a person or a team)
                                   ├─ provisioner: per-agent schema + role in the agents database
                                   ├─ launcher: start / stop agent runtimes (local processes | Kubernetes pods)
                                   ├─ reaper: hibernate idle agents
                                   └─ proxy: /v1/agents/{id}/threads/... → the agent's runtime (SSE streaming)
                                              │ bearer token
                                              ▼
                                   Agent runtime (Node, Pi Durable) ── its schema in PostgreSQL
```

## Verified so far

- `runtime/test/conformance.test.ts`: pi-durable 1.3.0's own storage conformance suite (24 cases) passes on PostgreSQL.
- Ownership: a second opener is refused; an owner whose backend is terminated can no longer read or write, and the
  next owner opens (`ownership.test.ts`).
- Isolation: an agent role cannot read another agent's schema (`ownership.test.ts`).
- A real Harness keeps its conversation and deduplicates retried requests across a process restart
  (`harness.test.ts`); a manual `kill -9` mid-reply resumed and completed the reply.

## Known limits and open items

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
