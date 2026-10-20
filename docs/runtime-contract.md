# The runtime contract (version 2)

What a process must do for the platform to run it as an agent. The platform's own runtime (`runtime/`, the library
`@pdp/runtime`) implements it, and so does any custom agent built with the library ([custom-agents.md](custom-agents.md)).
A runtime reports the version it implements at `/status` (`contractVersion`).

**Conformance.** `@pdp/runtime/testing` has the contract as HTTP tests anyone can run against a runtime, with Vitest or
Jest:

```ts
import { registerRuntimeConformance, registerStandbyConformance } from "@pdp/runtime/testing";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

registerRuntimeConformance({ describe, it, expect, beforeAll, afterAll }, "my runtime", async () => {
	const runtime = await startMyRuntime(); // its model must answer messages, with any text
	return { baseUrl: runtime.url, token: runtime.token, close: () => runtime.stop() };
});
```

`runtime/test/contract.test.ts` runs both suites against the platform's runtime process (`src/main.ts` on PostgreSQL,
as the local launcher starts it) and against the test kit's in-memory agent, and checks the process's exit codes.

## The process

A runtime starts in one of two modes, chosen by its environment:

- **As an agent**: with `AGENT_ID`. It reports its versions to the platform ([Starting](#starting-the-upgrade-check)),
  opens the agent's storage, takes ownership, resumes unfinished work and serves the agent's HTTP API.
- **In standby**: without `AGENT_ID`, with `RUNTIME_ACTIVATION_TOKEN` (Agent Sandbox warm pools). It serves `/healthz`
  and waits to be activated as one agent ([Standby and activation](#standby-and-activation)).

### Environment

| Variable | Meaning |
|---|---|
| `AGENT_ID` | The agent's ID (agent mode). |
| `AGENT_NAME`, `AGENT_INSTRUCTIONS` | Its name and its template version's instructions (defaults: "Assistant" and the assistant's default prompt). |
| `AGENT_SCHEMA`, `AGENT_DATABASE_URL` | Its schema and its own database role's connection URL. |
| `AGENT_CREATE_SCHEMA` | `true` creates the schema if missing (development only; the platform provisions it). |
| `RUNTIME_TOKEN` | The bearer token the platform presents on every request but `/healthz`. |
| `RUNTIME_ACTIVATION_TOKEN` | Standby: the token `/activate` requires. |
| `PORT`, `HOST` | Where to listen (default `4100` and `127.0.0.1`; the image sets `HOST=0.0.0.0`). |
| `MODEL_PROVIDER` | `demo` (a scripted model, the default), `catalog` or `openai-compatible`. |
| `MODEL_API`, `MODEL_BASE_URL`, `MODEL_API_KEY`, `MODEL_ID` | The model: its API (`openai-completions`, `openai-responses`, `anthropic-messages`), where to reach it (the platform's model proxy, or the provider in direct mode) and the key (the agent's gateway credential in proxy mode). |
| `MODEL_NAME`, `MODEL_PROVIDER_NAME`, `MODEL_CONTEXT_WINDOW`, `MODEL_MAX_TOKENS`, `MODEL_REASONING`, `MODEL_IMAGES`, `MODEL_HEADERS` | The model's description; `MODEL_HEADERS` is a JSON object of extra request headers (direct mode). |
| `MODEL_USAGE_URL`, `MODEL_USAGE_KEY` | Set when the gateway can't count the model's usage (direct mode, the platform's default model): the runtime reports it there. |
| `DEMO_TOKENS_PER_SECOND` | The demo model's speed. |
| `MCP_SERVERS` | JSON array of the template's MCP servers (slug, name, URL, headers in direct mode, tools). |
| `MCP_THREADS_URL`, `MCP_KEY` | Where to fetch the MCP servers apps gave a thread, and the credential. |
| `PLATFORM_GATEWAY_URL`, `PLATFORM_KEY` | Where to report thread status and tool approvals (and schedule), and the credential. |
| `TOOL_APPROVALS` | JSON `{tools, timeoutSeconds}`: the template version's tools (names, or patterns with `*`) that wait for a person's approval before they run ([approvals.md](approvals.md)). Absent: none. |
| `PLATFORM_SCHEDULES` | `true` when the agent's template version lets it set its own schedules (the library's schedule tools, [triggers.md](triggers.md#agent-made-schedules)); absent otherwise. |
| `AGENT_KIND`, `AGENT_KIND_SETTINGS` | The template version's kind (`triage`, `researcher`; absent: the assistant) and its settings (a JSON object), [first-party-agents.md](first-party-agents.md). The platform's runtime runs that composition; an unknown kind or settings it can't read are a misconfiguration (exit 2). A custom agent may ignore them. |

`MODEL_API_KEY` (in proxy mode), `MODEL_USAGE_KEY`, `MCP_KEY` and `PLATFORM_KEY` are the agent's gateway credential.
None of these values may be logged.

### Exit codes and signals

| Exit | When | The supervisor |
|---|---|---|
| 0 | `SIGTERM` or `SIGINT`: a graceful stop (hibernation). Work in flight stays pending in storage and resumes on the next start; ownership is released at once. | Starts it again on the next request. |
| 1 | Storage failed, or the database connection was lost. | Restarts it; the new process resumes from storage. |
| 2 | Misconfigured (neither `AGENT_ID` nor `RUNTIME_ACTIVATION_TOKEN`, a required variable missing). | Reports the error. |
| 3 | Another process owns the agent (its previous runtime hasn't stopped yet). | Retries later. |

### Starting: the upgrade check

Before it opens the agent's storage (in either mode, after activation in standby), a runtime with `PLATFORM_GATEWAY_URL`
and `PLATFORM_KEY` sends `POST {PLATFORM_GATEWAY_URL}/runtime/start` with `Authorization: Bearer <PLATFORM_KEY>` and
`{contractVersion, piDurableVersion}`. The platform records, per agent, the newest pi-durable version that has written
its storage, and answers:

| Answer | The runtime |
|---|---|
| `200` | Opens the storage and goes on. |
| `409 runtime_too_old` (`message`: why) | Never opens the storage. It serves `/healthz`, reports itself idle at `/status` with `refused: {code, message}`, and answers every other route `409 runtime_too_old` with the message, until it is stopped. |
| `404`, another `4xx`, or no answer after 3 tries | Goes on (a platform without the check; the platform checks `/status` after the start). |

After every start the platform also reads `/status`; a runtime whose `piDurableVersion` is older than the recorded one
(or absent, once one is recorded) is stopped, and the request that woke it answers `409 runtime_too_old`. Rolling a
runtime back after it has written an agent's data is not supported ([custom-agents.md](custom-agents.md#upgrades)).

### Storage and ownership

- All durable state is in the agent's schema, so any process started for the agent resumes where the last one stopped.
- **One owner at a time.** The runtime records its database session as the agent's owner (`pdp_ownership` in the
  agent's schema) and refuses to start while the recorded session exists (exit 3, or `409 owned_elsewhere` on
  activation). It takes ownership and writes on one connection, never re-established: when it is lost, the runtime
  exits 1, so its ability to write ends exactly when its ownership does (DESIGN.md, Agent storage and ownership).
- A graceful stop releases ownership before disconnecting, so the next start doesn't wait.

## The HTTP API

Only the platform calls a runtime. Every route but `/healthz` requires `Authorization: Bearer <RUNTIME_TOKEN>`
(compared in constant time; otherwise `401 unauthorized`). Success is `{"data": …}`, an error
`{"error": {"code", "message"}}`. Request bodies are JSON, at most 256 KiB.

Thread IDs and request IDs are 1-64 of `[A-Za-z0-9_-]`.

| Route | Answer |
|---|---|
| `GET /healthz` | `200 {"ok": true}`, without a token. In standby: `{"ok": true, "activated": false\|true}`. |
| `GET /status` | `200`, the [status](#status). |
| `GET /threads` | `200`, the threads, newest activity first: `{threadId, conversationId, title, createdAt, updatedAt}` (Unix ms; the title is the first line of the first message, at most 60 characters). |
| `POST /threads/{thread}/messages` | Body `{text, requestId?, whenBusy?, author?, announceAuthor?}`. Starts the thread if it doesn't exist, submits the message, `202 {"submissionId"}`. A retried `requestId` returns the first submission. `whenBusy`: `followUp` (default: after the reply in progress) or `steer` (joins it after its current tool round). `author` (`{kind: user\|service_account\|trigger, id, name, via?}`; `trigger`: an unattended run a schedule or webhook started, docs/triggers.md) is set by the platform and recorded on the message; `announceAuthor: true` tells the model who is speaking (`[Message from <name>]` before the text, `[Message from trigger <name>]` for a trigger). Errors: `400 invalid_thread_id`, `invalid_text` (empty or over 32,000 characters), `invalid_request_id`, `invalid_when_busy`, `invalid_request` (not JSON), `413`. |
| `GET /threads/{thread}` | `200`, the chat view: `{busy, messages, partial?, tools, documents?, approvals?}` (DESIGN.md, Threads: message metadata; [Custom state](#custom-state-in-the-view); a tool call carries `approval` once one was asked for it, and `approvals` lists the thread's pending ones, [approvals.md](approvals.md)). `?limit=&before=` pages it (`earlierCursor`); `400` for a bad `limit` or cursor. |
| `GET /threads/{thread}/documents/{kind}` | `200 {kind, version, revision, value}`: a conversation document the agent exposes. `404 document_not_found` for any kind it doesn't expose or the thread doesn't have (`pi.*` and `pdp.*` never), `422 document_too_large` over the author's bound (at most 1 MiB of JSON; the platform reads no more). |
| `GET /threads/{thread}/events` | Server-sent events, coalesced to at most 10 a second: `view` events (the whole view) by default; with `format=delta` (`v=1`) a `snapshot` and then changes, each with an `id` (its sequence; `Last-Event-ID` continues it). `once=true` sends the current state and ends; `limit` keeps the latest messages. A change to an exposed document is a view with its new revision, or a `document.updated` event; `approvals.set` replaces the pending approvals (`[]` clears them). `400 invalid_format`, `unsupported_version`. |
| `POST /threads/{thread}/stop` | Aborts the reply in progress and withdraws queued input: `202 {"ok": true}`. |
| `DELETE /threads/{thread}` | Stops and forgets the thread: `200 {"ok": true}`. |
| `POST /background/abort` | Body `{olderThanMs}` (a whole number of milliseconds, at least 0). Aborts the [background work](#status) that keeps the agent awake and started at least that long ago, with everything it owns, never a thread's reply in progress nor work waiting for a person; waits up to 5 seconds for it to end: `200 {"aborted", "settled"}` (how many units were aborted; whether they ended in time). Not activity. `400 invalid_older_than`. The platform calls it when background work passes its ceiling ([hibernation.md](hibernation.md)). |
| `GET /approvals?thread=` | `200`, the tool approval requests ([approvals.md](approvals.md)), pending first: `{id, threadId?, conversationId, taskId, callId, tool, arguments, summary?, requestedFor?, requestedAt, expiresAt?, status, decidedAt?, decidedBy?, reason?}`. |
| `POST /approvals/{id}` | Body `{decision: approve\|deny\|timeout\|cancel, reason?, decidedBy?: {kind: user\|service_account\|system, id?, name}}`: applies a decision to a pending request, `200 {id, status, applied}` (`applied: false` when it was already resolved; its status is unchanged). `404 approval_not_found`, `400 invalid_decision`. The platform sends it after a person decides or a request times out, waking the agent first. |

A thread the runtime doesn't have is `404 thread_not_found` on every thread route; any other path is
`404 not_found`.

### Custom state in the view

Additions to the view and the delta format are additive: a client that ignores a field it doesn't know keeps a correct
view of the rest, so they don't change the contract's version. Work on the view in parallel (approvals, #16) adds its
fields the same way.

- A tool result message (`role: "tool"`) and a current-round tool carry the tool's `details` (any JSON, at most 16 KiB)
  or `detailsOmitted: true` when they were larger; `tool.updated` and `tool.finished` carry `details` (null: none) and
  `detailsOmitted` when they change.
- `documents`: the exposed documents the thread has, `{kind: {revision}}` (absent when none). `revision` is a hash of the
  document's JSON. The delta event `document.updated` (`kind`, `revision`) sets one; a document that disappears comes
  as a new snapshot.

### Status

`GET /status` is how every platform replica learns whether an agent is idle (the reaper hibernates it when the activity
fields say so for `AGENTS_IDLE_TIMEOUT`; [hibernation.md](hibernation.md) has the rule):

| Field | Meaning |
|---|---|
| `agentId`, `name`, `model` | Who the runtime is, and its model (`provider/id`). |
| `busyThreads` | Threads with a reply in progress, except one that only waits for a person (version 2). |
| `waitingThreads` | Threads whose reply waits only for a person: a pending tool approval, no model request streaming and no tool running. They are not counted in `busyThreads` (version 2): hibernating such a runtime is safe, since the wait is in storage and resumes at the next start, and the decision wakes the agent ([approvals.md](approvals.md)). Reported for display; absent (0) for runtimes before it. |
| `pendingApprovals` | Tool approval requests waiting for a person. |
| `openStreams` | Event streams open now (through any platform replica). |
| `lastActivityAt` | Unix ms of the last request, stream, reply end or end of background work (not one the platform aborted). |
| `backgroundWork` | Version 2: `{tasks, oldestStartedAt, waitingForPerson}`. `tasks`: units of live background work that keep the agent awake, from pi-durable's task graph (background tasks, background subagents' runs, tool calls still running after their reply ended; not a thread's reply in progress, not work only waiting for a person), at most 1000. `oldestStartedAt`: Unix ms when the oldest of them started, `null` with none. `waitingForPerson`: live tasks waiting for a person's answer (at most 1000), counted nowhere else. |
| `contractVersion` | The contract version the runtime implements: `2`. |
| `piDurableVersion` | The pi-durable version it runs on (`1.3.0`). |
| `refused` | Only on a runtime the platform refused ([Starting](#starting-the-upgrade-check)): `{code, message}`. |

The platform reads at most 64 KiB, requires `busyThreads`, `openStreams` and `lastActivityAt` to be well-formed, and
accepts the versions only within bounds (a contract version from 1 to 1000; a pi-durable version that is a semantic
version with an optional short pre-release) and `backgroundWork` only when its counts are whole numbers from 0 (clamped
to 1000) and `oldestStartedAt` is a plausible Unix ms time whenever `tasks` is above 0, dropping anything else
(`internal/agenthost`). After a start it checks `piDurableVersion` against the version recorded for the agent (the
upgrade check); it stores nothing from `contractVersion` or `backgroundWork`. A runtime without `backgroundWork` is judged
by the version 1 rule.

**Waiting for a person.** A task of a kind the agent declares (`AgentDefinition.waitsForPerson`, and the platform's own
kinds) stands for a wait on a person's answer: it, and work that only waits on it, keeps the agent awake neither as a
busy thread nor as background work. Such a wait must survive hibernation ([hibernation.md](hibernation.md#waiting-for-a-person)).

## Standby and activation

A runtime in standby answers `503 not_activated` on every route but `/healthz` and `/activate`. The platform claims it
and sends `POST /activate` with `Authorization: Bearer <RUNTIME_ACTIVATION_TOKEN>` and the agent's configuration:

```json
{"agentId": "…", "name": "…", "instructions": "…", "schema": "agent_…", "databaseUrl": "postgres://…", "token": "…",
 "env": {"MODEL_PROVIDER": "catalog", "…": "…"}}
```

`schema` is an agent schema name (`agent_[a-z0-9_]{1,56}`), `databaseUrl` a PostgreSQL URL, `token` at least 16
characters, and `env` holds only the model, MCP and platform variables above (`MODEL_*`, `MCP_SERVERS`,
`MCP_THREADS_URL`, `MCP_KEY`, `PLATFORM_GATEWAY_URL`, `PLATFORM_KEY`, `TOOL_APPROVALS`, `PLATFORM_SCHEDULES`, `AGENT_KIND`,
`AGENT_KIND_SETTINGS`, `DEMO_TOKENS_PER_SECOND`); the body is at most 4 MiB.

| Answer | When |
|---|---|
| `200 {"activated": true, "agentId"}` | The runtime is now that agent and accepts only the agent's `token`. Activating again with the same agent and token answers 200 too (the platform may retry). |
| `400 invalid_config` | The body is not a valid configuration, or one the runtime can't make an agent from (an unknown `AGENT_KIND`): it stays in standby. |
| `401 unauthorized` | Wrong activation token. |
| `409 already_activated` | It already runs another agent. |
| `409 owned_elsewhere` | Another process still owns the agent; the runtime stays in standby and the platform retries. |
| `500 activation_failed` | The agent couldn't start; the process exits 1 and comes back in standby. |

## Reports to the platform

Best effort: each is retried a few times, and the next start reconciles what was missed.

- **Thread status**, with `Authorization: Bearer <PLATFORM_KEY>` to `PLATFORM_GATEWAY_URL`: at start
  `POST /threads/sync {threads: [ThreadStatus], at}` with every thread, and at the end of each reply
  `POST /threads/report ThreadStatus`, where `ThreadStatus` is `{threadId, title, createdAt, busy, lastReplyAt?,
  lastError?, at}` (`lastError`: why the reply failed or went unanswered, for people). The platform keeps the newest by
  `at`, so it can list an agent's threads without waking it.
- **Tool approvals** ([approvals.md](approvals.md)), with the same credential: each new request (and again whenever
  the waiting call is resumed) `POST /approvals/report {id, threadId?, tool, callId, arguments, summary?, requestedFor?,
  requestedAt, status}` (`status: cancelled` when the call went away), answered with `{id, status, reason?, decidedBy?,
  expiresAt}`: the platform's expiry, and its decision if it has one, which the runtime applies. At start
  `POST /approvals/sync {approvals: [report], at}` with every pending request, answered with the decisions the platform
  has for them (a list of the same answers); the platform cancels pending requests asked before `at` that the list
  lacks. `arguments` is a redacted, bounded view (the platform redacts again).
- **Usage**, when `MODEL_USAGE_URL` is set: batches of `{requests, input, output, cacheRead, cacheWrite}` to it, with
  `Authorization: Bearer <MODEL_USAGE_KEY>`, a moment after responses end and when the runtime stops.
- **Thread MCP servers**, when `MCP_THREADS_URL` is set: `GET MCP_THREADS_URL?thread=<thread>` (before each message;
  without `thread` at start, for every thread) with `Authorization: Bearer <MCP_KEY>` returns `{data: [server]}`.
- **Schedules**, when `PLATFORM_SCHEDULES` is `true`: the agent's own reminders and recurring work, with
  `Authorization: Bearer <PLATFORM_KEY>`: `POST /schedules`, `GET /schedules`, `DELETE /schedules/{id}` on
  `PLATFORM_GATEWAY_URL` ([triggers.md](triggers.md#agent-made-schedules)). Not retried: the tool answers the model.

## Versions of this contract

| Version | Change |
|---|---|
| 1 | First written version: what runtimes of platform 0.1 to 0.3 did, plus `contractVersion` and `piDurableVersion` at `/status`. Additions since (both sides may ignore them): the upgrade check at start (`/runtime/start`, `refused` at `/status`), tool `details` and exposed `documents` in the view, `document.updated`, `GET /threads/{thread}/documents/{kind}`, and tool approvals (`TOOL_APPROVALS`, `/approvals`, `waitingThreads` and `pendingApprovals` at `/status`, the view's `approvals`, the `approvals.set` delta event, the approval reports), and `PLATFORM_SCHEDULES` with the gateway's schedule routes (sent only to agents whose version allows it), and agent kinds (`AGENT_KIND`, `AGENT_KIND_SETTINGS`). |
| 2 | `backgroundWork` at `/status`, `POST /background/abort`, and `busyThreads` no longer counts a reply that only waits for a person (platform 0.4). Tool approvals add `waitingThreads` and `pendingApprovals` (approval waits are already left out of `busyThreads`). A platform reads a version 1 runtime as before. |

The version goes up when a runtime and a platform of different versions can't work together (a route or field one side
needs and the other lacks, a changed meaning). Additions both sides may ignore keep it.
