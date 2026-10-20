# Roadmap

Status: **proposed 2026-10-18**, after v0.1.0; **revised 2026-10-19** for custom agents and unattended runs. It is an order of work, not a schedule. Each item is a GitHub issue
(label `roadmap`, milestone per phase). Sizes are relative effort: **S** a few days, **M** a week or two, **L** several
weeks. An item that needs a design gets a note in `docs/` before code.

The items come from the **Known limitations** of [v0.1.0](releases/v0.1.0.md), gaps found while writing the
documentation, and the first app's requests (a chat app using OAuth Connect).

## Why this order

1. **Deploying comes first.** The first thing someone trying Pi Durable Platform does is install it. Manifests,
   several replicas, metrics and the two security gaps make that safe without the maintainer's help.
2. **Then the apps built on it.** Paging, smaller event streams, steering, safe retries and app-level events and
   usage are what an app with hundreds of people needs.
3. **Then agents that run on their own, and agents you can build.** The runtime as a library, triggers (schedules and
   webhooks), hibernation that understands background work, first-party triage and research agents, and tool
   approvals. Unattended runs use the team's credentials, never a person's.
4. **Then files, memory and limits that hold**, once agents run unattended.
5. **Last, coding agents.** A workspace with files and a shell, kept apart from the agent's credentials.

**Shipped:** [v0.2.0](releases/v0.2.0.md) (2026-10-19) delivered phase v0.2 (items A to G and V).

Already fixed after v0.1.0, in [v0.1.1](releases/v0.1.1.md): a set of small UI bugs, `doctor` checking the
agents database, and a console path for platform staff to a shared team's agents.

Bugs are tracked separately: issues labelled `bug`, with the next fix release's milestone. Fixed in
[v0.1.2](releases/v0.1.2.md): the approval screen's app logo (#25) and the agent page's status badge after a wake (#27).

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
| v0.4 | X. Custom agents, step 1: the runtime as a library, with documents, hooks and tasks, a stable facade and a test kit | L | [#28](https://github.com/ncecere/pi-durable-platform/issues/28) |
| v0.4 | O. Triggers: schedules and webhooks wake agents and start runs | L | [#15](https://github.com/ncecere/pi-durable-platform/issues/15) |
| v0.4 | Z1. Background-aware hibernation: an agent with work running in the background is not hibernated | M | [#34](https://github.com/ncecere/pi-durable-platform/issues/34) |
| v0.4 | Z2. First-party agents: a triage agent and a deep researcher, built with the library | L | [#35](https://github.com/ncecere/pi-durable-platform/issues/35) |
| v0.4 | P. Tool approvals: a person approves sensitive tool calls before they run | L | [#16](https://github.com/ncecere/pi-durable-platform/issues/16) |
| v0.5 | N. Files and images in messages, stored on S3-compatible object storage | L | [#14](https://github.com/ncecere/pi-durable-platform/issues/14) |
| v0.5 | Q. Runtime resources per template version | S | [#17](https://github.com/ncecere/pi-durable-platform/issues/17) |
| v0.5 | R. Enforce budgets: monthly cost limits for personal workspaces and teams | M | [#18](https://github.com/ncecere/pi-durable-platform/issues/18) |
| v0.5 | S. Limits for shared teams: maximum agents and maximum running | S | [#19](https://github.com/ncecere/pi-durable-platform/issues/19) |
| v0.5 | Z3. An assistant with memory: notes that last across conversations | M | [#36](https://github.com/ncecere/pi-durable-platform/issues/36) |
| v0.5 | Y. Custom agents, step 2: platform admins register agent runtimes, and a template version picks one | M | [#29](https://github.com/ncecere/pi-durable-platform/issues/29) |
| v0.6 | Z4. Workspaces: files and a shell for coding agents | L | [#37](https://github.com/ncecere/pi-durable-platform/issues/37) |
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

**Shipped in [v0.2.0](releases/v0.2.0.md):** a Helm chart (published signed to `oci://ghcr.io/ncecere/charts/pi-durable-platform`) and a Kustomize base generated from it, with overlays for the Kubernetes and Agent Sandbox launchers, NetworkPolicies (and a Cilium flavour), and a kind smoke test in CI.

### B. Several platform replicas: share the idle reaper's view through PostgreSQL (M, [#2](https://github.com/ncecere/pi-durable-platform/issues/2))

**Limitation:** One platform replica: the idle reaper only knows the event streams open on its own replica.

Rate limits, thread events and OAuth state are already shared through PostgreSQL, but each replica's reaper only sees its own open event streams and can hibernate an agent whose stream is open on another replica.

- Record agent activity (open streams, last request) in PostgreSQL with a short heartbeat, or elect one reaper with an advisory lock and have replicas report activity.
- Make wake-up single-flight across replicas (an advisory lock per agent around launch).
- An integration test with two platform processes against one database; a PodDisruptionBudget and `replicas: 2` in the chart.

**Done when:** Two replicas behind one Service pass the acceptance suite, and an agent with an open stream on either replica is never hibernated.

**Shipped in [v0.2.0](releases/v0.2.0.md):** two replicas are supported. The runtime reports its open streams and the reaper believes it, one replica reaps at a time, and starting an agent is serialised across replicas. Design: [docs/replicas.md](replicas.md).

### C. Metrics and tracing: a Prometheus endpoint, dashboards and OpenTelemetry traces (M, [#3](https://github.com/ncecere/pi-durable-platform/issues/3))

**Limitation:** No metrics endpoint, dashboards or tracing.

Operators can't see wake times, running agents, proxy latency, errors or usage without reading logs.

- `/metrics` on a separate listener: HTTP requests, agent wakes (count and duration), running and hibernated agents, warm pool claims, model and MCP proxy requests (status, latency, tokens), OAuth token issues, reaper actions.
- Optional OpenTelemetry traces (OTLP) from the API through the gateway to model and MCP upstreams.
- Grafana dashboards and a ServiceMonitor in the chart.

**Done when:** The chart ships a dashboard showing wake latency, running agents, proxy traffic and errors from the platform's own metrics.

**Shipped in [v0.2.0](releases/v0.2.0.md):** Prometheus `/metrics` on its own listener, optional OTLP/HTTP traces, a Grafana dashboard and example alerts, and chart support for a ServiceMonitor, a scraper NetworkPolicy and a dashboard ConfigMap. The runtime doesn't read `traceparent` yet.

### D. A `rekey` command that re-seals every secret under the current encryption key (S, [#4](https://github.com/ncecere/pi-durable-platform/issues/4))

**Limitation:** No re-encryption command: keep ENCRYPTION_KEY_PREVIOUS until every sealed value has been saved again.

Rotating ENCRYPTION_KEY means keeping the previous key indefinitely, because sealed provider keys, MCP headers and thread servers are only re-sealed when someone saves them.

- `pi-durable-platform rekey`: re-seal every sealed column (provider keys and headers, MCP server headers, thread MCP servers, anything else sealed) under the current key in batches, idempotent and resumable; `--dry-run` counts values per key.
- `doctor` reports values still sealed under the previous key.

**Done when:** After `rekey`, `doctor` reports nothing under the previous key and the platform starts without ENCRYPTION_KEY_PREVIOUS.

**Shipped in [v0.2.0](releases/v0.2.0.md):** `pi-durable-platform rekey` re-seals every sealed column under the current key, and `doctor` counts values still under the previous one.

### E. Platform-chosen thread IDs, and thread MCP servers only for threads that exist (S, [#5](https://github.com/ncecere/pi-durable-platform/issues/5))

**Limitation:** Thread IDs are chosen by clients: thread MCP servers can be set on a thread that doesn't exist yet, and a later private thread with that ID inherits them.

Anyone who may use an agent can pre-seed MCP servers on a thread ID someone else later uses for a private thread.

- `POST /v1/agents/{id}/threads` creates a thread with a platform-generated ID (and optional visibility), returning it; clients may keep choosing IDs for compatibility.
- Setting thread MCP servers requires the thread to exist and the caller to be allowed on it (its creator for private threads); servers set before the thread existed are dropped when a different person starts it.

**Done when:** A test shows a person can't attach MCP servers to another person's future private thread.

**Shipped in [v0.2.0](releases/v0.2.0.md):** `POST /v1/agents/{id}/threads` returns a platform-chosen ID, MCP servers can be set only on a thread that exists or that the same call creates, and reserved threads are pruned after 30 days.

### F. Re-check IdP group membership: end sessions, keys and connected apps of people no longer allowed (S, [#6](https://github.com/ncecere/pi-durable-platform/issues/6))

**Limitation:** OIDC_ALLOWED_GROUPS is applied at sign-in only (found while writing the docs).

Removing someone from the allowed IdP group leaves their sessions, personal API keys and connected apps working until they expire.

- Re-check groups when a session is refreshed and on a schedule where the IdP supports it (userinfo/introspection or SCIM later); suspend access for people no longer in an allowed group.
- Suspending a person (by an admin or by this check) revokes their sessions, personal API keys and OAuth grants, audited.

**Done when:** A person removed from the allowed group loses console, API key and connected app access within one check interval, and the audit log says why.

**Shipped in [v0.2.0](releases/v0.2.0.md):** the platform asks the identity provider every 15 minutes whether people are still in an allowed group, and ends their sessions, keys and connected apps when not, with a grace period when the provider can't answer.

### G. Harden the agents database: per-agent lock namespaces and cleanup of agent roles (M, [#7](https://github.com/ncecere/pi-durable-platform/issues/7))

**Limitation:** Advisory locks are database-wide; database roles are server-wide and outlive a dropped agents database.

Any role in the agents database could block another agent's lock key, which is acceptable only for trusted templates, and agent roles linger after an agents database is dropped.

- Derive lock keys so an agent role can only take its own keys (or move ownership locking to a table with row-level security), and test that an agent role can't block another agent.
- `doctor` lists orphaned agent roles; a `prune-roles` command drops them.

**Done when:** A test shows one agent's role can't take another agent's ownership lock, and orphaned roles are reported and removable.

**Shipped in [v0.2.0](releases/v0.2.0.md):** an agent's ownership is a row in its own schema, `prune-roles` and `prune-agent-storage` clean up, and `doctor` reports orphan roles and whether agent roles can reach the platform database.

### V. Configuration reference: .env.example lists every setting, and config comments are accurate (S, [#23](https://github.com/ncecere/pi-durable-platform/issues/23))

**Limitation:** .env.example doesn't list every setting (AGENTS_NAMESPACE, AGENTS_RUNTIME_CLASS and others are missing).

Operators learn some settings only from the docs site or the code, and the API_KEY_PEPPER comment says it covers personal API keys only (it also covers service account keys and OAuth tokens and codes).

- A test that compares the settings internal/config reads with .env.example, so a new setting can't be added without documenting it.
- Add the missing settings with defaults and one-line explanations; fix the APIKeyPepper comment.

**Done when:** CI fails when a setting is read but not in .env.example, and .env.example lists all of them today.

**Shipped in [v0.2.0](releases/v0.2.0.md):** `.env.example` lists every setting, and a test keeps it complete.

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

## v0.4: Agents that run on their own

Agents wake only when called, and every agent is the same program: a template changes the instructions, the model and the MCP tools, never the agent's code. This phase lets people build agents with their own tools, state and guards, lets agents work unattended on a schedule or when an event arrives, and ships two first-party agents built that way: a triage agent and a deep researcher. Unattended runs use the team's credentials, never a person's.

### X. Custom agents, step 1: the runtime as a library, with documents, hooks and tasks, a stable facade and a test kit (L, [#28](https://github.com/ncecere/pi-durable-platform/issues/28))

**Limitation:** No way to build your own agent: every agent is the one program in `runtime/` (instructions, a model and MCP tools are all a template changes).

pi-durable supports much more than the runtime uses: custom documents (state next to the transcript), hooks (guards, approvals), tasks, subagents, an execution environment, code mode and settings. The runtime installs one extension with one tool, passes no settings, hooks or documents, and is an application, not a package, so someone who wants an agent with its own tools, state, guards or background work has nothing to start from. pi-durable is also marked experimental and changed its API between 1.1 and 1.2, so authors need a layer that absorbs that.

- Make `runtime/` importable: `startAgent` and its parts (PostgreSQL storage, the thread view, thread records and status reports, the MCP client, usage reporting, the standby and activation server), with an injection point for extensions (tools, prompt sections, hooks, tasks), documents, `conversationCreated` and settings. The platform's own agent is the default composition.
- The runtime reapplies the extension list on every message with an array selection, so an extension left out of that list (a guard hook, say) silently does not run. The library owns that list and always includes the author's extensions.
- A stable facade over pi-durable (define an agent, tools, documents, hooks, a subagent helper with request-id idempotence, since pi-durable ships subagents only as examples). The platform pins pi-durable, and the runtime reports its pi-durable version and a contract version at `/status`.
- Upgrades are checked: documents migrate when read and a newer stored version rejects older code, so the platform refuses to start an agent on a runtime older than the one that last wrote its data, and says why. Rolling a runtime back after it has written is not supported.
- Custom state reaches apps in two ways: structured `details` on tool results in thread messages, and a read and watch endpoint for an agent's documents. A document kind opts in to being exposed, and reading it needs the same access as reading the thread, so private threads stay private.
- A test kit: a fake model, in-memory storage, restart tests (a restart in the middle of a turn, a tool call and a subagent), a check that a replayed run neither repeats nor loses a tool call or a subagent's result, and pi-durable's storage conformance suite.
- A starter project (scaffold, one tool, one document, one approval hook, tests, a Dockerfile on the runtime image) and a local loop: run the agent against the local launcher and a local PostgreSQL and chat with it in the console's Test tab.
- A written contract for what a runtime must do (the `/threads` API, `/status` including busy threads, open streams and background work, standby and `/activate`, thread status and usage reports, ownership, exit on lost storage) with a conformance test anyone can run.
- Design note first: the trust model for team-written code (it runs with the agent's database role and gateway key, inside the agents' network boundary, so an external integration is an MCP server and code adds logic, state and guards) and what step 2 must check.

**Done when:** A person scaffolds an agent with a tool, a document and an approval hook, runs it locally against the console, passes the conformance and restart tests, and an app reads the agent's document, without changing the platform.

### O. Triggers: schedules and webhooks wake agents and start runs (L, [#15](https://github.com/ncecere/pi-durable-platform/issues/15))

**Limitation:** No scheduled wake-ups: a hibernated agent wakes only on a request.

Agents hibernate, and pi-durable has no timer an extension's task can sleep on (its scheduler waits internally only for retry back-off), so an agent cannot wake itself. The control plane is always running, so it owns the triggers: it decides when work starts, wakes the agent, and records what happened. Triage, research and an assistant that reminds people all need this.

- Triggers are platform records per agent: schedules (cron with a time zone) and webhooks (one inbound address per trigger, a signed secret, an optional filter on the payload). A fire submits an input to a thread, or to a new thread, with a template for the message.
- Delivery is idempotent: each fire has an id that becomes the input's pi-durable `requestId`, so a retry or a second replica cannot run it twice. One replica fires a due schedule (an advisory lock), and a missed fire follows a per-schedule policy (skip, or run once on recovery).
- Run records, one per fire: the trigger, the thread, start and finish, status, cost and a link. They appear in the API, in a Runs view in the console, and in the audit log with the trigger named.
- Unattended runs act with the team's credentials (its model providers and MCP servers), never as a person: an OAuth Connect token's person is not borrowed, and a team owner or admin creates the trigger.
- Limits that hold: concurrent runs per agent, a maximum run time, a cost budget per trigger and per agent, a pause switch per trigger and per agent, and back-off after repeated failures. Team-wide enforcement is #18 and #19.
- Results: the thread is always the record. A trigger can also send a signed completion callback (HMAC, through the address guard; run id, status, an excerpt of the final text and a link, not the transcript). Agents can post results themselves through MCP tools such as a chat server.
- Agent-made schedules: a `schedule` tool in the library (#28) registers a trigger for the agent itself through the gateway: only for itself, within the limits, and listed and removable in the console.
- Design note first: webhook authentication and replay protection, the fire-id scheme, and how a run that needs approval (#16) waits.

**Done when:** A team schedule runs daily and a webhook starts a run; each appears in Runs with its cost and fires its callback; a runaway trigger is stopped by the limits; an agent sets its own reminder and is woken for it; no run acts as a person.

### Z1. Background-aware hibernation: an agent with work running in the background is not hibernated (M, [#34](https://github.com/ncecere/pi-durable-platform/issues/34))

**Limitation:** The idle reaper counts a thread as busy only while it has a live run; background work doesn't count.

The runtime reports an agent as busy when a thread has a live run, and the reaper adds open event streams and recent activity. Background tasks, background subagents and a command a tool started are not runs in a thread, so an agent could be hibernated in the middle of them. Triggers (#15) and agents built with the library (#28) make such work normal.

- `/status` reports live background work, taken from pi-durable's task graph (tasks that are running or waiting, owned conversations), next to busy threads and open streams, and the reaper treats it as busy.
- A run-time ceiling so a stuck task cannot keep an agent awake forever: past the per-agent maximum (from the limits in #15) the work is aborted and the agent may hibernate, with an audit entry.
- What happens to work that is waiting for a person (an approval, #16, or a question) is written down: it does not hold the agent awake, and resumes when the answer arrives and the agent wakes.
- Metrics and the agent page show why an agent is not hibernating (a run, a stream, background work).

**Done when:** An agent with a background task running is not hibernated until it finishes or reaches its ceiling, one waiting only for a person is hibernated and resumes on the answer, and the console says what is keeping an agent awake.

### Z2. First-party agents: a triage agent and a deep researcher, built with the library (L, [#35](https://github.com/ncecere/pi-durable-platform/issues/35))

**Limitation:** Every agent is the same assistant: a template changes its instructions, model and MCP tools.

Two common jobs need more than instructions and tools: keeping track of what has been seen and asking before acting (triage), and planning, splitting work and keeping sources (research). Building them with the library (#28) proves that the library is enough, without private APIs, and gives teams something to use before they write code.

- A template version has an agent kind (`assistant` by default). Kinds ship in the standard runtime image, so there is no new image or warm pool.
- **Triage:** reads issues and discussions through MCP servers, keeps what it has seen and decided in a document, drafts labels, replies and summaries, and asks for approval (#16) before it writes anywhere. It runs on triggers (#15): a webhook for a new issue, a schedule for a sweep.
- **Deep researcher:** writes a plan into a document, runs subagents in parallel (foreground for short questions, background for long ones), keeps a sources document, compacts as it goes, and answers with a report in the thread. Reports as files wait for #14. It respects the run budget from #15.
- Both expose their state (the queue, the plan, the sources) through the documents endpoint and `details` (#28), and the console renders it on the agent page.
- The console's template editor offers the kind, with the settings each one has (for example, which MCP servers triage may write through).
- Docs: what each does, its limits, and how to build a variant.

**Done when:** A team makes a template of each kind in the console and runs one end to end: triage on a webhook with an approval before it comments, and a research run with subagents that ends in a report and a sources list. Neither uses an API that a custom agent could not.

### P. Tool approvals: a person approves sensitive tool calls before they run (L, [#16](https://github.com/ncecere/pi-durable-platform/issues/16))

**Limitation:** No tool approvals: agents call allowed tools without asking anyone.

Templates can only allow or forbid a tool; there's no 'ask first'.

- Template versions mark tools (built-in or MCP) as `approve`; the runtime pauses the call and the platform records a pending approval.
- Approvals in the API, the event stream and the console (who may approve: the thread's creator, or team roles), with timeouts and audit.
- An unattended run that needs approval waits, and the agent can hibernate meanwhile; the request carries the decision, the options and the evidence the agent gave, with a timeout, and the answer is recorded in the audit log. Hooks written with the library (#28) can request approval too.

**Done when:** A call to an approval-required tool waits until someone allowed approves or denies it, and the conversation shows the decision.

## v0.5: Files, memory and limits

Once agents run unattended, limits have to hold (budgets, maximum agents, maximum running). Agents also need somewhere to put files and reports, resources that fit the template, and a memory that lasts across conversations. Platform admins can register extra agent runtimes once the contract has settled.

### N. Files and images in messages, stored on S3-compatible object storage (L, [#14](https://github.com/ncecere/pi-durable-platform/issues/14))

**Limitation:** Text only: no files, images or artifacts, and no object storage.

People can't give an agent a document or screenshot, and agents can't return files.

- Uploads to S3-compatible storage (per-team prefixes, size and type limits, sealed credentials), referenced from messages; images passed to models that accept them.
- Agent-created files (artifacts) saved through a runtime tool and downloadable through the platform with the thread's access rules.
- Retention follows the thread; deletes remove objects.

**Done when:** A person sends an image and a PDF to an agent and downloads a file the agent produced, with private-thread rules applied to the files.

### Q. Runtime resources per template version (S, [#17](https://github.com/ncecere/pi-durable-platform/issues/17))

**Limitation:** Fixed runtime resources: 50m CPU / 160 MiB requested, 1 CPU / 512 MiB limit.

Heavier agents (large tools, big contexts) can't get more memory; light ones can't ask for less.

- Template versions choose a size from platform-defined classes (small, medium, large), applied by the Kubernetes and Agent Sandbox launchers (warm pools per class).

**Done when:** An agent on a 'large' template version runs with that class's requests and limits.

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

### Z3. An assistant with memory: notes that last across conversations (M, [#36](https://github.com/ncecere/pi-durable-platform/issues/36))

**Limitation:** An agent remembers only within a thread; nothing carries over.

A personal assistant is useful because it remembers preferences and facts. Memory shared across threads can leak what one person said in a private thread to another person who uses the same team agent, so whose memory it is has to be decided before any is built.

- Design note first: whose memory it is (a person's, a thread's or the agent's), what a private thread may write to it, and who may read, edit and delete it.
- A memory document with `remember`, `recall` and `forget` tools in the library (#28); in a personal workspace the memory belongs to the person, in a team agent it is per person or per agent as the team chooses. Private threads write to shared memory only if the person says so.
- Memory is visible and editable in the console by whoever it belongs to, shown in the thread as `details`, and removed with the person or the agent.
- It works with schedules (#15), so an assistant can remind and check back.

**Done when:** An assistant remembers a fact in one conversation and uses it in another, its owner can read and delete it, and nothing from a private thread reaches anyone else's memory.

### Y. Custom agents, step 2: platform admins register agent runtimes, and a template version picks one (M, [#29](https://github.com/ncecere/pi-durable-platform/issues/29))

**Limitation:** Agent runtime image is one platform-wide setting (AGENTS_IMAGE): templates can't use different agent code.

The first-party agents (triage, researcher) ship inside the standard runtime and a template version can choose among them, so most teams need no new image. Someone who builds an agent with the library (#28) outside this repository needs a way to run it from a template with versions, upgrades, a catalog and the same isolation and accounting. Code that runs in the agent's process runs with its database role, so who may register it is a security decision, not a convenience.

- Platform admins register agent runtimes: an image pinned by digest, with its contract version, its pi-durable version, the result of the conformance test (run by the platform against a throwaway instance) and, optionally, a cosign signature from an allowed identity. A template version names one registered runtime (or a first-party kind), recorded in the version so upgrades and rollbacks work as they do for instructions and models.
- Before an agent starts on a runtime, the platform checks the runtime's pi-durable and contract versions against what the agent's data was last written with (#28).
- Launchers: the Kubernetes and Agent Sandbox launchers start the version's image. A warm pool exists per registered image (created by the platform), with the cold-start cost stated for one without a pool.
- Teams registering their own images waits for stronger sandboxing (#21) and a decision on review; until then a template that uses a registered runtime can be shared only after a platform admin's review, as templates already are.
- Isolation is unchanged (agents reach only their database, the gateway port and DNS; their own role and schema), and resource classes (#17) apply.
- Audit entries for who registered which image and who set it on which version, and a docs section: the contract, a worked example, and what the platform does and does not protect.

**Done when:** An admin registers an agent image, a team's template version uses it, upgrades and rolls it back like any version, and the platform shows which code each agent runs. Depends on #28, #17 and #1; self-service registration by teams depends on #21.

## v0.6: Coding agents

A coding agent needs a workspace with files and a shell. The shell must not run where the agent's database credentials are, and the files must outlive hibernation, so this is a phase of its own and the last of the agent-capability work.

### Z4. Workspaces: files and a shell for coding agents (L, [#37](https://github.com/ncecere/pi-durable-platform/issues/37))

**Limitation:** Agents have no files and no shell: the runtime installs no `read`, `write`, `edit` or `bash` tools.

pi-durable's coding tools work through an execution environment, and the runtime provides none. Running `bash` inside the runtime's own pod would give a model-written command the agent's database credentials and gateway key, and files in a pod vanish when the agent hibernates. A coding agent needs a different shape.

- Design note first: the threat model and the shape below.
- The shell and the files live in a separate workspace sandbox (an Agent Sandbox claim per workspace) with its own pod, no database credentials and no gateway key; the runtime's execution environment talks to it.
- Workspace storage survives hibernation: a volume per workspace, or a git clone restored on wake; the choice and its limits are written down.
- Egress for git and package registries goes through an allowlist or a proxy set by the platform, never open internet, with an audit trail.
- One writer at a time per workspace, and long commands (builds, tests) run without holding a model slot and wake the agent when they finish, through the wake path from #15.
- Resource classes (#17) for workspaces, an idle timeout for the workspace itself, and metrics.
- A first-party `coding` kind built with the library (#28) with `read`, `write`, `edit` and `bash`, plan mode and approvals (#16) for risky commands.

**Done when:** A coding agent reads and edits a repository, runs its tests and commits, its workspace survives hibernation, and the shell cannot read the agent's credentials. Depends on #14, #15, #16, #17, #21 and #28.

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
