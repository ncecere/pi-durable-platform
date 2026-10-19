# Roadmap

Status: **proposed 2026-10-18**, after v0.1.0. It is an order of work, not a schedule. Each item is a GitHub issue
(label `roadmap`, milestone per phase). Sizes are relative effort: **S** a few days, **M** a week or two, **L** several
weeks. An item that needs a design gets a note in `docs/` before code.

The items come from the **Known limitations** of [v0.1.0](releases/v0.1.0.md), gaps found while writing the
documentation, and the first app's requests (a chat app using OAuth Connect).

## Why this order

1. **Deploying comes first.** The first thing someone trying Pi Durable Platform does is install it. Manifests,
   several replicas, metrics and the two security gaps make that safe without the maintainer's help.
2. **Then the apps built on it.** Paging, smaller event streams, steering, safe retries and app-level events and
   usage are what an app with hundreds of people needs.
3. **Then what agents can do.** Files, scheduled work and tool approvals change what agents are trusted with.
4. **Then budgets and limits that hold**, once agents can run unattended.

Already fixed after v0.1.0, in [v0.1.1](releases/v0.1.1.md): a set of small UI bugs, `doctor` checking the
agents database, and a console path for platform staff to a shared team's agents.

Bugs are tracked separately: issues labelled `bug`, with the next fix release's milestone (now **v0.1.2**: the
approval screen's app logo, #25, and the agent page's status badge after a wake, #27).

## Overview

| Phase | Item | Size | Issue |
| --- | --- | --- | --- |
| v0.2 | A. Deployment manifests: a Kustomize base and a Helm chart with RBAC and NetworkPolicies | L | [#1](https://github.com/ncecere/pi-durable-platform/issues/1) |
| v0.2 | B. Several platform replicas: share the idle reaper's view through PostgreSQL | M | [#2](https://github.com/ncecere/pi-durable-platform/issues/2) |
| v0.2 | C. Metrics and tracing: a Prometheus endpoint, dashboards and OpenTelemetry traces | M | [#3](https://github.com/ncecere/pi-durable-platform/issues/3) |
| v0.2 | D. A `rekey` command that re-seals every secret under the current encryption key | S | [#4](https://github.com/ncecere/pi-durable-platform/issues/4) |
| v0.2 | E. Platform-chosen thread IDs, and thread MCP servers only for threads that exist | S | [#5](https://github.com/ncecere/pi-durable-platform/issues/5) |
| v0.2 | F. Re-check IdP group membership: end sessions, keys and connected apps of people no longer allowed | S | [#6](https://github.com/ncecere/pi-durable-platform/issues/6) |
| v0.2 | G. Harden the agents database: per-agent lock namespaces and cleanup of agent roles | M | [#7](https://github.com/ncecere/pi-durable-platform/issues/7) |
| v0.2 | V. Configuration reference: .env.example lists every setting, and config comments are accurate | S | [#23](https://github.com/ncecere/pi-durable-platform/issues/23) |
| v0.3 | H. Paging and search for threads and conversations | S | [#8](https://github.com/ncecere/pi-durable-platform/issues/8) |
| v0.3 | I. Incremental event streams: send changes, not the whole conversation view | M | [#9](https://github.com/ncecere/pi-durable-platform/issues/9) |
| v0.3 | J. Steering: send a message that changes the reply in progress | S | [#10](https://github.com/ncecere/pi-durable-platform/issues/10) |
| v0.3 | K. Safe agent creation for people and apps: idempotency and keys in one's own workspace | M | [#11](https://github.com/ncecere/pi-durable-platform/issues/11) |
| v0.3 | L. OAuth Connect for apps with many people: login_hint, prompt=none, app-level events and usage | M | [#12](https://github.com/ncecere/pi-durable-platform/issues/12) |
| v0.3 | M. Move an agent, with its conversations, to another team or workspace | M | [#13](https://github.com/ncecere/pi-durable-platform/issues/13) |
| v0.3 | W. Usage reports for several agents at once, grouped by agent and day | S | [#24](https://github.com/ncecere/pi-durable-platform/issues/24) |
| v0.4 | N. Files and images in messages, stored on S3-compatible object storage | L | [#14](https://github.com/ncecere/pi-durable-platform/issues/14) |
| v0.4 | O. Scheduled wake-ups: agents run work at a time or on a schedule | M | [#15](https://github.com/ncecere/pi-durable-platform/issues/15) |
| v0.4 | P. Tool approvals: a person approves sensitive tool calls before they run | L | [#16](https://github.com/ncecere/pi-durable-platform/issues/16) |
| v0.4 | Q. Runtime resources per template version | S | [#17](https://github.com/ncecere/pi-durable-platform/issues/17) |
| v0.5 | R. Enforce budgets: monthly cost limits for personal workspaces and teams | M | [#18](https://github.com/ncecere/pi-durable-platform/issues/18) |
| v0.5 | S. Limits for shared teams: maximum agents and maximum running | S | [#19](https://github.com/ncecere/pi-durable-platform/issues/19) |
| later | T. Run without Kubernetes: a Docker Compose deployment with a bundled identity provider | L | [#20](https://github.com/ncecere/pi-durable-platform/issues/20) |
| later | U. Stronger agent sandboxing: gVisor or Kata RuntimeClasses | M | [#21](https://github.com/ncecere/pi-durable-platform/issues/21) |

## v0.2: Run it anywhere, safely

People who try v0.1.0 will first try to deploy it. Today that means writing their own Kubernetes manifests and NetworkPolicies, running a single replica, and having no metrics. This phase makes a deployment outside the maintainer's installation straightforward and safe to operate, and closes two security gaps the v0.1.0 notes list.

### A. Deployment manifests: a Kustomize base and a Helm chart with RBAC and NetworkPolicies (L, [#1](https://github.com/ncecere/pi-durable-platform/issues/1))

**Limitation:** No deployment manifests in the repository (no Helm chart, Kustomize base or NetworkPolicies).

Operators write their own Deployment, RBAC and NetworkPolicies from the docs. Agent isolation (agents reach only PostgreSQL, the gateway port and DNS) depends on NetworkPolicies nobody ships, and the docs' examples have only been checked against one cluster.

- `deploy/kustomize/`: base (platform Deployment and Services, gateway Service, ServiceAccount, Role/RoleBinding with exactly the verbs the launchers need, NetworkPolicies for the platform and the agents namespace) and overlays for the `kubernetes` and `agent-sandbox` launchers (SandboxTemplate, SandboxWarmPool).
- `deploy/helm/pi-durable-platform`: the same, with values for images (by digest), database URLs from Secrets, OIDC, ingress, launcher, warm pool size and resources.
- CI renders both and validates them (kubeconform), and a kind-based smoke test installs the chart, provisions an agent and checks the isolation rules.
- The docs' Operate section points at the chart; the release workflow publishes the chart (OCI, signed) alongside the images.

**Done when:** A new cluster runs Pi Durable Platform from the chart with agent isolation enforced, verified by an automated smoke test in CI.

### B. Several platform replicas: share the idle reaper's view through PostgreSQL (M, [#2](https://github.com/ncecere/pi-durable-platform/issues/2))

**Limitation:** One platform replica: the idle reaper only knows the event streams open on its own replica.

Rate limits, thread events and OAuth state are already shared through PostgreSQL, but each replica's reaper only sees its own open event streams and can hibernate an agent whose stream is open on another replica.

- Record agent activity (open streams, last request) in PostgreSQL with a short heartbeat, or elect one reaper with an advisory lock and have replicas report activity.
- Make wake-up single-flight across replicas (an advisory lock per agent around launch).
- An integration test with two platform processes against one database; a PodDisruptionBudget and `replicas: 2` in the chart.

**Done when:** Two replicas behind one Service pass the acceptance suite, and an agent with an open stream on either replica is never hibernated.

### C. Metrics and tracing: a Prometheus endpoint, dashboards and OpenTelemetry traces (M, [#3](https://github.com/ncecere/pi-durable-platform/issues/3))

**Limitation:** No metrics endpoint, dashboards or tracing.

Operators can't see wake times, running agents, proxy latency, errors or usage without reading logs.

- `/metrics` on a separate listener: HTTP requests, agent wakes (count and duration), running and hibernated agents, warm pool claims, model and MCP proxy requests (status, latency, tokens), OAuth token issues, reaper actions.
- Optional OpenTelemetry traces (OTLP) from the API through the gateway to model and MCP upstreams.
- Grafana dashboards and a ServiceMonitor in the chart.

**Done when:** The chart ships a dashboard showing wake latency, running agents, proxy traffic and errors from the platform's own metrics.

### D. A `rekey` command that re-seals every secret under the current encryption key (S, [#4](https://github.com/ncecere/pi-durable-platform/issues/4))

**Limitation:** No re-encryption command: keep ENCRYPTION_KEY_PREVIOUS until every sealed value has been saved again.

Rotating ENCRYPTION_KEY means keeping the previous key indefinitely, because sealed provider keys, MCP headers and thread servers are only re-sealed when someone saves them.

- `pi-durable-platform rekey`: re-seal every sealed column (provider keys and headers, MCP server headers, thread MCP servers, anything else sealed) under the current key in batches, idempotent and resumable; `--dry-run` counts values per key.
- `doctor` reports values still sealed under the previous key.

**Done when:** After `rekey`, `doctor` reports nothing under the previous key and the platform starts without ENCRYPTION_KEY_PREVIOUS.

### E. Platform-chosen thread IDs, and thread MCP servers only for threads that exist (S, [#5](https://github.com/ncecere/pi-durable-platform/issues/5))

**Limitation:** Thread IDs are chosen by clients: thread MCP servers can be set on a thread that doesn't exist yet, and a later private thread with that ID inherits them.

Anyone who may use an agent can pre-seed MCP servers on a thread ID someone else later uses for a private thread.

- `POST /v1/agents/{id}/threads` creates a thread with a platform-generated ID (and optional visibility), returning it; clients may keep choosing IDs for compatibility.
- Setting thread MCP servers requires the thread to exist and the caller to be allowed on it (its creator for private threads); servers set before the thread existed are dropped when a different person starts it.

**Done when:** A test shows a person can't attach MCP servers to another person's future private thread.

### F. Re-check IdP group membership: end sessions, keys and connected apps of people no longer allowed (S, [#6](https://github.com/ncecere/pi-durable-platform/issues/6))

**Limitation:** OIDC_ALLOWED_GROUPS is applied at sign-in only (found while writing the docs).

Removing someone from the allowed IdP group leaves their sessions, personal API keys and connected apps working until they expire.

- Re-check groups when a session is refreshed and on a schedule where the IdP supports it (userinfo/introspection or SCIM later); suspend access for people no longer in an allowed group.
- Suspending a person (by an admin or by this check) revokes their sessions, personal API keys and OAuth grants, audited.

**Done when:** A person removed from the allowed group loses console, API key and connected app access within one check interval, and the audit log says why.

### G. Harden the agents database: per-agent lock namespaces and cleanup of agent roles (M, [#7](https://github.com/ncecere/pi-durable-platform/issues/7))

**Limitation:** Advisory locks are database-wide; database roles are server-wide and outlive a dropped agents database.

Any role in the agents database could block another agent's lock key, which is acceptable only for trusted templates, and agent roles linger after an agents database is dropped.

- Derive lock keys so an agent role can only take its own keys (or move ownership locking to a table with row-level security), and test that an agent role can't block another agent.
- `doctor` lists orphaned agent roles; a `prune-roles` command drops them.

**Done when:** A test shows one agent's role can't take another agent's ownership lock, and orphaned roles are reported and removable.

### V. Configuration reference: .env.example lists every setting, and config comments are accurate (S, [#23](https://github.com/ncecere/pi-durable-platform/issues/23))

**Limitation:** .env.example doesn't list every setting (AGENTS_NAMESPACE, AGENTS_RUNTIME_CLASS and others are missing).

Operators learn some settings only from the docs site or the code, and the API_KEY_PEPPER comment says it covers personal API keys only (it also covers service account keys and OAuth tokens and codes).

- A test that compares the settings internal/config reads with .env.example, so a new setting can't be added without documenting it.
- Add the missing settings with defaults and one-line explanations; fix the APIKeyPepper comment.

**Done when:** CI fails when a setting is read but not in .env.example, and .env.example lists all of them today.

## v0.3: Apps at scale

The first app (a chat app using OAuth Connect) reaches people's personal workspaces and team agents. With hundreds of people it needs paging, smaller event streams, steering, safe retries and an app-level view of events and usage. These are the requests in the chat app's second round of questions.

### H. Paging and search for threads and conversations (S, [#8](https://github.com/ncecere/pi-durable-platform/issues/8))

**Limitation:** No paging: thread lists and conversations come back whole.

People with hundreds of conversations, and long conversations, load everything at once.

- `GET /v1/agents/{id}/threads?limit=&cursor=` (newest activity first) and `?q=` on titles.
- `GET .../threads/{thread}?before=&limit=` to page messages backwards; the event stream keeps sending the latest window.

**Done when:** The console and the API page threads and messages; existing clients that don't pass parameters get today's behaviour.

### I. Incremental event streams: send changes, not the whole conversation view (M, [#9](https://github.com/ncecere/pi-durable-platform/issues/9))

**Limitation:** Whole-view events: each change sends the whole conversation view (coalesced to 10 a second).

Long conversations make every token update resend the whole transcript.

- A versioned delta format on `GET .../events` (opt-in with a parameter or header): message added, message updated (partial text appended), tool started and finished, run state, with a sequence number and a full snapshot on reconnect.
- Live metadata (elapsed time on a running tool) in the deltas.

**Done when:** A client using deltas renders the same view as a full snapshot, with traffic proportional to new tokens.

### J. Steering: send a message that changes the reply in progress (S, [#10](https://github.com/ncecere/pi-durable-platform/issues/10))

**Limitation:** Messages always queue behind a reply (whenBusy followUp); steering isn't exposed.

People can't redirect an agent mid-reply; they have to stop it and resend.

- `whenBusy: steer | followUp` on `POST .../messages`, passed to pi-durable; document how a steering message appears in the view.
- The console's test chat offers it while a reply runs.

**Done when:** A steering message sent during a reply is applied to that reply, and the view shows it in order.

### K. Safe agent creation for people and apps: idempotency and keys in one's own workspace (M, [#11](https://github.com/ncecere/pi-durable-platform/issues/11))

**Limitation:** Service-managed (by-key) agents are service-account only; agent creation has no idempotency key.

An app acting for a person (OAuth Connect) can't create an agent idempotently: a retry after a timeout can create two.

- An `Idempotency-Key` header on `POST /v1/teams/{team}/agents`, remembered per caller for 24 hours.
- Allow `PUT .../agents/by-key/{key}` for a person (session, API key or app token) in their own personal workspace, owned by them, so apps can ensure one agent per purpose.

**Done when:** Retrying agent creation with the same key returns the first agent, for service accounts, people and apps.

### L. OAuth Connect for apps with many people: login_hint, prompt=none, app-level events and usage (M, [#12](https://github.com/ncecere/pi-durable-platform/issues/12))

**Limitation:** OAuth Connect doesn't support login_hint or prompt=none; the only scope is agents.

Apps can't reconnect silently, pre-select the account, hold one event stream for everyone who connected them, or report their people's usage.

- `login_hint` passed to the IdP and checked against the signed-in person; `prompt=none` returning `login_required` / `consent_required` instead of a page.
- An app-level event stream: a connected app subscribes once (client authentication with a per-app secret or private_key_jwt) to thread events of every person who connected it.
- Usage per connected app (the platform records which grant made each request) for the app's own admin view, limited to people who connected it.
- Consider a read-only scope (`agents:read`).

**Done when:** A chat app reconnects silently, keeps one event stream for all its connected people, and shows their usage, with tests for each.

### M. Move an agent, with its conversations, to another team or workspace (M, [#13](https://github.com/ncecere/pi-durable-platform/issues/13))

**Limitation:** No agent transfer between teams or workspaces.

Agents created in the wrong place, or service-managed agents a person wants to keep, can't be moved; conversations are lost if they start over.

- `POST /v1/agents/{id}/move {team}` for people allowed on both sides (and platform admins); the agent keeps its schema and conversations; template and model availability is re-checked in the new team.
- Private threads stay their creator's; shared threads become visible to the new team (shown before confirming).

**Done when:** An agent moved between teams keeps its conversations and is usable only under the new team's rules, audited on both sides.

### W. Usage reports for several agents at once, grouped by agent and day (S, [#24](https://github.com/ncecere/pi-durable-platform/issues/24))

**Limitation:** Usage grouped by day takes one agentId, so an app shows several agents' daily usage with one call per agent.

An app's usage page (a person's agents by day) makes one request per agent.

- A repeatable `agentId` filter on the team usage report, and `groupBy=agent,day`.

**Done when:** One request returns daily usage for several agents, with tests for the filters and grouping.

## v0.4: Agents that do more

Agents are text-only, wake only when called, and use every tool they're given without asking. This phase adds files, scheduled work, approvals for sensitive tools and per-template resources.

### N. Files and images in messages, stored on S3-compatible object storage (L, [#14](https://github.com/ncecere/pi-durable-platform/issues/14))

**Limitation:** Text only: no files, images or artifacts, and no object storage.

People can't give an agent a document or screenshot, and agents can't return files.

- Uploads to S3-compatible storage (per-team prefixes, size and type limits, sealed credentials), referenced from messages; images passed to models that accept them.
- Agent-created files (artifacts) saved through a runtime tool and downloadable through the platform with the thread's access rules.
- Retention follows the thread; deletes remove objects.

**Done when:** A person sends an image and a PDF to an agent and downloads a file the agent produced, with private-thread rules applied to the files.

### O. Scheduled wake-ups: agents run work at a time or on a schedule (M, [#15](https://github.com/ncecere/pi-durable-platform/issues/15))

**Limitation:** No scheduled wake-ups: a hibernated agent wakes only on a request.

Work an agent schedules for later, or recurring jobs (a daily summary), doesn't run while it sleeps.

- The runtime reports pi-durable's next timer; the platform stores it and wakes the agent when due (survives restarts, one wake across replicas).
- Team-defined schedules (cron) that post a message to a thread, with an audit trail and limits.

**Done when:** An agent that schedules a reminder wakes from hibernation and runs it on time, and a team schedule runs daily.

### P. Tool approvals: a person approves sensitive tool calls before they run (L, [#16](https://github.com/ncecere/pi-durable-platform/issues/16))

**Limitation:** No tool approvals: agents call allowed tools without asking anyone.

Templates can only allow or forbid a tool; there's no 'ask first'.

- Template versions mark tools (built-in or MCP) as `approve`; the runtime pauses the call and the platform records a pending approval.
- Approvals in the API, the event stream and the console (who may approve: the thread's creator, or team roles), with timeouts and audit.

**Done when:** A call to an approval-required tool waits until someone allowed approves or denies it, and the conversation shows the decision.

### Q. Runtime resources per template version (S, [#17](https://github.com/ncecere/pi-durable-platform/issues/17))

**Limitation:** Fixed runtime resources: 50m CPU / 160 MiB requested, 1 CPU / 512 MiB limit.

Heavier agents (large tools, big contexts) can't get more memory; light ones can't ask for less.

- Template versions choose a size from platform-defined classes (small, medium, large), applied by the Kubernetes and Agent Sandbox launchers (warm pools per class).

**Done when:** An agent on a 'large' template version runs with that class's requests and limits.

## v0.5: Budgets and limits

Cost is recorded but not enforced, and shared teams have no limits. Once agents run unattended (scheduled wake-ups, files), platform admins need limits that hold.

### R. Enforce budgets: monthly cost limits for personal workspaces and teams (M, [#18](https://github.com/ncecere/pi-durable-platform/issues/18))

**Limitation:** Budgets are not enforced: the personal workspace monthly budget is recorded only.

A runaway agent or a busy person can spend without limit; cost is only reported.

- Monthly budgets for teams and personal workspaces (defaults and overrides), checked by the model proxy before each request; over budget, requests fail with a clear error and the thread shows it.
- Warnings at thresholds (in the console, and to team owners), and usage of models the proxy doesn't see counted from runtime reports.

**Done when:** A team over its monthly budget gets `budget_exceeded` on model requests until the next month or a raised limit, with warnings before.

### S. Limits for shared teams: maximum agents and maximum running (S, [#19](https://github.com/ncecere/pi-durable-platform/issues/19))

**Limitation:** Shared teams have no limits on agent count or running agents.

Only personal workspaces have limits; one team can fill the cluster.

- The personal workspace limits (max agents, max running) applied to shared teams too, with platform defaults and per-team overrides in Admin › Teams.

**Done when:** A team at its running limit gets `too_many_running` when waking another agent, as personal workspaces do.

## Later

Wanted, not scheduled. Each needs a design note first.

### T. Run without Kubernetes: a Docker Compose deployment with a bundled identity provider (L, [#20](https://github.com/ncecere/pi-durable-platform/issues/20))

**Limitation:** Agent runtimes need Kubernetes (or the single-process local launcher).

Self-hosters without Kubernetes or an IdP can't run Pi Durable Platform beyond a local trial.

- A container launcher (Docker API or a sandbox runtime such as celld) for Compose deployments, with the same isolation goals.
- A Compose file with PostgreSQL and a small OIDC provider (Pocket ID or Dex) preconfigured.

**Done when:** `docker compose up` gives a working platform with sign-in and agents on one host.

### U. Stronger agent sandboxing: gVisor or Kata RuntimeClasses (M, [#21](https://github.com/ncecere/pi-durable-platform/issues/21))

**Limitation:** Agents run as hardened pods, but in the node's kernel.

Untrusted templates would benefit from a kernel boundary.

- Document and test AGENTS_RUNTIME_CLASS with gVisor and Kata, including the Agent Sandbox warm pool, and measure wake time.

**Done when:** The docs describe a tested gVisor setup with its wake-time cost.

## Out of scope

Pi Durable Platform runs and governs agents built with Pi's durable package. It is not a model gateway, a chat app,
a workflow engine or a vector database, and the roadmap adds none of those: apps such as a chat interface are built
on its API.
