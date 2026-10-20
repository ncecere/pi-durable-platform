# Triggers: schedules and webhooks that start runs

Design note for roadmap item O ([#15](https://github.com/ncecere/pi-durable-platform/issues/15)). Agents hibernate,
and pi-durable has no timer an agent could sleep on, so an agent cannot wake itself. The control plane is always
running: it owns the triggers, decides when work starts, wakes the agent and records what happened.

## What a trigger is

A trigger is a platform record of one agent (`agent_triggers`), of one of two kinds:

- **Schedule**: a five-field cron expression (or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`) in an IANA
  time zone (`Europe/Paris`; default `UTC`), parsed with `github.com/robfig/cron/v3` (pinned at v3.0.1). The
  platform keeps the next fire time (`next_fire_at`), computed in the zone, so daylight saving time is the zone's.
  `@every` is refused: it ignores the zone. The finest step is a minute.
- **Webhook**: one inbound address per trigger, `POST <APP_URL>/v1/hooks/<hookId>`, where `hookId` is `h_` and 128
  random bits, with a secret only the platform and the sender know, and an optional filter on the payload.

Either kind sends a message to a thread of the agent: a new thread at each fire (the default), or one fixed thread
(a client-chosen ID that is shared, or not used yet). The message comes from a **template**.

**Templates are text with placeholders, never code.** `{{payload}}` (the whole body: compact JSON, or the text as
sent), `{{payload.a.b.0.c}}` (a field by path: names and array indexes, at most 10 deep), `{{trigger.name}}`,
`{{trigger.kind}}`, `{{fire.id}}` and `{{fire.time}}` (RFC 3339, UTC). Nothing else is recognised; an unknown root is
refused when the trigger is saved, and `payload` placeholders on a schedule too. A missing field is empty, a string
is inserted as is, anything else as compact JSON; each insertion is cut at 4,000 characters and the message at
32,000 (the runtime's limit). There are no loops, conditions, functions or escapes: a payload cannot make the
template do anything but insert text.

**The filter** is one condition: the field at a path equals a value (`{"path": "action", "equals": "opened"}`). A
string field is compared as is, numbers, booleans and null by their JSON text (`42`, `true`, `null`). A payload that
is not JSON, or lacks the field, does not match. A delivery that doesn't match is answered `202 {"matched": false}`
and leaves no record.

## Webhook authentication and replay protection

The inbound address is public by necessity: anyone who learns it can send to it. What they can't do without the
secret is start anything.

- **The secret** (`pdpwh_` and 256 random bits) is shown once, when the trigger is created or the secret rotated
  (`POST …/triggers/{id}/secret {"which": "webhook"}`; the old one stops working at once). The platform must compute
  the same HMAC to check a delivery, so the secret is sealed with `ENCRYPTION_KEY` (associated data
  `trigger_webhook:<trigger id>`), not digested; `rekey` re-seals it like every other sealed column. Only its first
  characters (`webhookSecretPrefix`) are shown again.
- **The signature**: the sender sends `X-PDP-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256(secret, "<t>." + body)>`
  (the scheme Stripe and others use; several `v1=` are accepted, so a sender may sign with two secrets while it
  rotates). The platform reads the body (at most 256 KiB), checks every `v1` in constant time, and refuses (401
  `invalid_signature`) a missing or malformed header, a wrong signature, and a `t` more than **5 minutes** away from
  its clock, in either direction. The signature binds the time to the body, so an old delivery can't be re-dated.
- **Seen signatures**: a delivery's fire ID is derived from its trigger and signature
  (`wh_` + base64url(SHA-256(trigger ID, ":", signature))[:32]), and `trigger_runs.fire_id` is unique. A replay within
  the tolerance window finds the first delivery's run and is answered with it (`replayed: true`) without starting
  anything; after the window the timestamp check refuses it. So the run records are the seen-signature store, and
  they outlive the window by far. A sender that retries a delivery it signed once gets the same answer; one that
  signs again (a new `t`) is a new delivery, as it should be: senders should put their own delivery ID in the body if
  they need end-to-end de-duplication.
- **Unknown, deleted or paused**: an unknown `hookId` is 404 (the ID is 128 random bits; its existence reveals
  nothing). The signature is checked before anything else is read or recorded, including the trigger's pause state.
- **Flooding**: each address is rate-limited (120 deliveries a minute, shared by the replicas through the rate limit
  store; 429 beyond). A sender with the secret can still start runs at that rate: the limits below bound what they
  cost.
- **Never logged**: the body, the signature, the secret. The request log names the route pattern only
  (`/v1/hooks/{hookId}`), never the path.

### Threat model

| Threat | Answer |
|---|---|
| Someone finds the address (a log, a referrer, a leaked config) | Without the secret nothing starts: 401 |
| A captured delivery is replayed | Same fire ID: the first run is returned, nothing starts; after 5 minutes, 401 |
| A captured delivery is re-dated or its body changed | The signature covers both: 401 |
| A sender with the secret floods the address | Rate limit per address; concurrency, budgets and back-off per trigger and agent |
| The payload carries instructions for the model (prompt injection) | Inherent: a webhook's payload is untrusted text given to a model that may call the team's tools. The template should frame it as data; connect senders the team trusts; tool approvals (#16) let a person approve sensitive tool calls. The run never acts as a person (below), so the worst case is bounded by the team's own tools and budgets |
| A template is crafted to execute something | Templates only insert text (above) |
| Timing on the comparison | `hmac.Equal` |

## Fire IDs and idempotent delivery

Every fire has an ID that is the message's pi-durable `requestId` (the runtime's message API takes it, and pi-durable
answers a repeated `requestId` with the first submission instead of running it again), and the unique key of its run
record:

| Fire | ID | Why it is stable |
|---|---|---|
| Schedule | `sc_<trigger id, 32 hex>_<scheduled Unix seconds>` | Every replica computes the same ID for the same scheduled time |
| Webhook | `wh_` + 32 characters of the signature's digest | A replayed delivery has the same signature |
| Run now (a person, from the console) | `mn_` + 128 random bits | Each click is a new fire |

A fire inserts its run (`ON CONFLICT (fire_id) DO NOTHING`), with the rendered message sealed (`message_enc`, until
it has been delivered) and the thread it goes to already chosen (a new thread's ID is drawn at fire time), in the
same transaction as its audit entry. Delivery happens after the commit: the replica that fired takes a short lease on
the run (`next_attempt_at`), wakes the agent and relays the message with the fire ID as `requestId`. If that replica
dies, or the agent can't be woken, the run stays `pending` and the trigger loop of any replica delivers it again
after the lease, to the same thread with the same `requestId`: a message that did reach the runtime is not run twice.
Delivery gives up after 5 attempts or 15 minutes (`failed`, `delivery_failed`). A refusal that won't change (the agent
was turned off, the fixed thread became someone's private thread or has an app's MCP servers) fails at once.

**One replica fires a due schedule.** Every replica runs the trigger loop (`TRIGGERS_INTERVAL`, default 10 s); each
pass takes the advisory lock `pdp.trigger-loop` (a connection of its own, `pg_try_advisory_lock`, as the reaper does) and
skips when another replica holds it. Correctness doesn't rest on the lock: due schedules are read `FOR UPDATE SKIP
LOCKED` and advanced in the transaction that inserts the run, and the fire ID is unique, so two replicas firing at the
same moment make one run.

**Missed fires.** A schedule is missed when the platform was not running (or the loop could not reach the database)
at its time: its `next_fire_at` is more than `max(2 × TRIGGERS_INTERVAL, 1 minute)` in the past. Each schedule has a
policy:

- `skip` (default): nothing runs; one `skipped` run (reason `missed`, the latest missed time) records that it happened.
- `run_once`: one run for the latest missed time, however many were missed (a daily report missed for three days
  runs once, not three times).

Either way the next fire is the first one after now. Pausing and resuming a trigger (or the agent's triggers) also
starts from now: a pause never piles up missed fires.

## Runs

One record per fire (`trigger_runs`): the trigger (its name and kind are kept, so a deleted trigger's runs still say
whose they were), the agent and its team at the time, how it was fired (`schedule`, `webhook`, `manual`), the thread,
the scheduled time, created, started (the message accepted) and finished times, status and reason, cost, and the
callback's delivery state.

| Status | Meaning |
|---|---|
| `pending` | Recorded, not delivered yet (or being retried) |
| `running` | The runtime accepted the message; the reply is in progress |
| `succeeded` | The thread's reply ended without an error |
| `failed` | The reply ended with an error, or delivery failed (`reason`) |
| `stopped` | A limit stopped it: `max_run_time` or `budget` (the thread was aborted) |
| `skipped` | Nothing ran: `trigger_paused`, `agent_paused`, `team_archived`, `concurrency`, `budget`, `backoff`, `missed` |

**When a run ends.** The runtime reports each reply's end to the platform (`/threads/report`, `agent_threads`: busy,
last error, the time). A running run is finished when its thread is no longer busy with a status reported at or after
the run started; with an error it failed. The trigger loop checks running runs every pass.

Work a reply leaves running in the background (a background subagent) is not part of the run: neither its run time
nor its cost covers it. The deep researcher keeps its subagents in the foreground in a triggered run for that reason
([first-party-agents.md](first-party-agents.md#budgets-and-ceilings)).

**Cost** is the agent's model usage (`model_usage`, priced at the models' current prices, as in the usage reports)
between the run's start and its end. Usage is recorded per agent, not per thread (the runtime does not tag model
requests with their thread), so a run that overlaps other work of the same agent (people talking to it, another run)
counts that work too. It never counts less than the run cost, which is the safe side for a budget; a precise figure
needs the runtime to tag requests (the same gap as usage per connected app). While a run is in progress its cost is
updated every pass.

**API.** `GET /v1/agents/{agentId}/runs` (newest first; `?triggerId=`, `?status=`, `limit` and `cursor`),
`GET /v1/agents/{agentId}/runs/{runId}`, and `GET /v1/teams/{team}/runs` (every agent of the team the caller may
use). Each run has `link`, the console's address of its thread. The console shows them in the agent page's Runs tab.

**Audit.** Every run is recorded when it is fired, as `trigger.run` (actor `system`, or the person for a run started
with Run now: `trigger.fire`), naming the trigger, agent, thread, how it was fired and its first status (a skipped
run's reason). A limit stopping a run is `trigger.run_stop`, an automatic pause `trigger.auto_pause`. Changes to
triggers are `trigger.create`, `trigger.update`, `trigger.delete` and `trigger.secret_rotate`; to the agent's limits
`trigger_settings.update`. Never in an entry: secrets, the payload, the message.

## Unattended runs act as no person

- The message's author is the trigger: `{kind: "trigger", id: <trigger id>, name: <trigger name>}`, a new author kind
  in the chat view (the runtime accepts it; older runtimes drop an author they don't know and show none). The model
  is told `[Message from trigger <name>]`.
- A trigger's thread is shared (the team's record), started by nobody (`createdBy` is null). A fixed thread must be
  shared: a private thread is someone's, and a reserved one too. A thread an app gave MCP servers to (with its
  person's headers) is refused, so a run never uses a person's tools; new threads have none.
- The run's model and MCP requests are the agent's, through the gateway, with the team's model providers and MCP
  servers. The run's sender is recorded as "not through an app" (`agent_run_senders` without a grant), so its usage
  is never attributed to a connected app, and no OAuth grant or person's credential is involved anywhere.
- **MCP identity tokens** (#57): the MCP proxy's identity token for a triggered run follows that work's rule for
  runs nobody is present for. In a shared team it names no person. In a personal workspace it still names the
  workspace's owner, with `person_present: false`: the workspace and its agent are theirs, but nobody is at the
  keyboard. Triggers change nothing here. They send no person to the gateway and record the run's sender without a
  grant, and an MCP server that needs a person present can refuse a token with `person_present: false`.
- **Who manages triggers**: the agent's team's **owners and admins**, in a browser session (not service accounts,
  personal API keys or apps: an unattended run that spends the team's money with its credentials is a person's
  decision in the console); in a personal workspace, its owner (an owner of it). Editors and members read triggers,
  settings and runs (everyone who may use the agent: service accounts too); platform admins and auditors read them,
  as they read agents. Editors are left out on purpose: they change agents, but a trigger is standing permission to
  spend without anyone present, as adding a model provider is (also owners' and admins'). Service-managed agents (an
  app's agent for one of its users) take no triggers: they aren't the team's to use.
- An archived team's triggers are read-only, and its schedules don't fire. A turned-off agent's runs fail
  (`agent_disabled`) and count as failures.
- **Moving an agent** deletes its triggers (their addresses stop working; the move plan counts them, `triggers`): a
  trigger was set up with the old team's limits, callback and secrets in mind. Runs stay where they happened.

## Limits that hold

Per agent (`GET`/`PUT /v1/agents/{agentId}/trigger-settings`, owners and admins), within platform ceilings:

| Limit | Default | Ceiling (setting) |
|---|---|---|
| Runs at once (pending and running) | 1 | `TRIGGERS_MAX_CONCURRENT_RUNS` (5) |
| Maximum run time | the ceiling | `TRIGGERS_MAX_RUN_TIME` (30m) |
| Cost per UTC day of the agent's runs | none | |
| Pause all of the agent's triggers | off | |

Per trigger: a pause switch, a cost budget per UTC day, and back-off.

- **At fire time**, in the transaction that records the run, under a transaction lock per agent (so replicas and
  concurrent webhooks see each other's runs): paused (trigger or agent), back-off, runs at once, and budgets (the
  trigger's and the agent's runs' cost today) are checked; a fire that fails one is recorded as `skipped` with the
  reason. Paused schedules don't record skipped runs at each tick: their next time just moves on.
- **While a run is in progress**, every pass: past the maximum run time, or once the trigger's or the agent's cost
  today exceeds its budget, the thread is aborted (the runtime's stop) and the run is `stopped`
  (`max_run_time`, `budget`), audited as `trigger.run_stop`. Aborting stops the thread's reply in progress, a person's
  too on a fixed thread.
- **Back-off**: a failed run, and a run stopped for its run time, count as failures of the trigger: the next fire
  waits `min(1 minute × 2^(failures-1), 1 hour)` (fires meanwhile are `skipped`, `backoff`), and after
  `TRIGGERS_PAUSE_AFTER_FAILURES` (5) failures in a row the trigger is paused (`pausedReason: failures`), audited as
  `trigger.auto_pause`. A success resets the count. Run now ignores the trigger's pause and back-off (a person is
  trying it), never the agent's pause, the runs at once or the budgets.
- Team-wide budgets and running limits are #18 and #19; personal workspaces' running limit already applies to waking
  (a run that can't wake its agent is retried, then fails).

## Results and the completion callback

The thread is always the record. A trigger may also have a **completion callback**: an https URL the team chooses,
called once its run ends (any status but `skipped`).

- `POST` with JSON `{"type": "run.finished", "runId", "triggerId", "triggerName", "agentId", "threadId", "status",
  "reason", "scheduledFor", "startedAt", "finishedAt", "costUsd", "excerpt", "link"}`: `excerpt` is at most 500
  characters of the run's last assistant message (read from the runtime when the callback is sent; null when it can't
  be read), `link` the console's address of the thread. Never the transcript, the message or the payload.
- Signed like webhooks with the trigger's **callback secret** (`pdpcb_`, 256 bits, shown once, sealed, rotated with
  `{"which": "callback"}`): `X-PDP-Signature: t=…,v1=…` over `"<t>." + body`, plus `X-PDP-Event: run.finished` and
  `X-PDP-Delivery: <run id>` (a receiver de-duplicates by it).
- Through the address guard (`internal/teamnet`, the private networks platform admins allow for team services): https
  only, checked when saved (422 `address_not_allowed`) and at every connection; no redirects, no proxy, 10 s timeout,
  the response body not read beyond 4 KiB and never stored or shown.
- 2xx is delivered. Anything else is retried with back-off (30 s, 1, 2, 4, 8 minutes), then `failed` (the run shows
  `callbackStatus`). Errors are logged with the run and trigger IDs, never the URL's response.

## Approvals (#16)

A run that reaches a tool needing a person's approval waits on a person, not working ([approvals.md](approvals.md)):
it is unattended (its author is a trigger), so the team's owners and admins decide. The supervisor doesn't stop it for
`max_run_time` while its thread has a pending approval: the approval's own expiry (the template version's timeout,
default 24 h) bounds the wait and denies the call, and the run goes on. The idle reaper may hibernate the agent
meanwhile (the runtime's `/status` says `waitingThreads`, #34); the decision wakes it and the run goes on in the same
thread; the callback is sent when it ends. Nothing in this design assumes a run ends without a person: `succeeded`
means the thread's reply ended, whenever that is. Not built: a `waiting` run status of its own (the run shows
`running`).

## Agent-made schedules

An agent sets its own reminders and recurring work with the runtime library's `schedule` tool (with `list_schedules`
and `cancel_schedule`). Each one is an ordinary schedule trigger of that agent, marked as made by it.

**Who may.** A template version's `agentSchedules` flag (off by default; the template editor's *Schedules* setting)
lets its agents schedule. The platform then gives the runtime `PLATFORM_SCHEDULES=true`, and the library adds the
tools (an extension named `pdp-schedules`) to every thread after the definition's own extensions: the default
assistant and any custom agent built with `defineAgent` (`schedules: false` leaves them out). The gateway checks the
version again when the agent schedules. Service-managed agents (an app's agent for one of its users) and an archived
team's agents can't.

**The tool.** `schedule({when, timezone?, message, thread?, name?})`: `when` is an ISO 8601 date and time for a
one-time reminder (`2026-10-21T09:00` in `timezone`, default UTC, or with an offset) or a cron expression for
recurring work (the schedules' rules above); `message` is what the agent is told when it fires, as plain text (never a
template: nothing in it is a placeholder); `thread` is `this` (the default) or `new`. The answer says when it fires,
where, and its ID; a refusal is the platform's message, as an error the model reads (what to change).

**The gateway API.** The runtime calls the gateway with its own credential (`PLATFORM_KEY`, as it reports thread
status), never a person's: the credential names the agent, and every operation is on that agent's own agent-made
triggers.

| Route | What |
|---|---|
| `POST /schedules` | Body `{at \| cron, timezone?, message, thread?, name?, threadId?}` (`threadId`: the thread the tool was called in, which the library knows). `201 {schedule, where: "this" \| "new", whyNotHere?}` |
| `GET /schedules` | The agent's own schedules (not the triggers people made) |
| `DELETE /schedules/{id}` | Cancel one of them; another agent's, or a person's, is `404 trigger_not_found` |

A schedule is `{id, name, once, cron?, timezone, message, threadId, originThreadId, nextFireAt, paused, pausedReason?}`.
These routes are the gateway's (the agents' listener), not the public API.

**Limits**, refused with a message for the model: at most `TRIGGERS_AGENT_MAX` (20) agent-made schedules per agent
(`too_many_schedules`), within the 50 triggers an agent may have; a recurring one fires at most once every
`TRIGGERS_AGENT_MIN_INTERVAL` (5 minutes; `too_frequent`, checked over its next fires); a one-time reminder is in the
future (`time_passed`) and at most a year ahead (`too_far_ahead`); a message of 1-8,000 characters. Every trigger limit
holds for their runs: the agent's pause, runs at once, budgets, back-off and the automatic pause.

**Where it fires.** In the thread it was set in when that thread may take a trigger's message (the rules above: not
reserved, no app's MCP servers, not private, and known to the platform), otherwise in a new thread at every fire;
`whyNotHere` says why. One relaxation: in a personal workspace, a private thread its owner started may take it (the
workspace and its agent are the owner's; their MCP identity token names them with `person_present: false` while the
thread works on a triggered run). The thread is checked again at each fire: one that can no longer take it (made
private, given an app's servers) gets a new thread instead.

**One-time reminders** (`once`) store their time in `next_fire_at` and, so the record reads as any schedule, a cron
naming the same minute in its zone. They use the `run_once` missed-fire policy (a reminder missed while the platform
was down still runs, once). When it fires the reminder has no next time; it removes itself when its run ends (a run
that is skipped, by a limit or a paused agent, removes it at once), audited as `trigger.delete` by the system
(`reason: fired`). Paused and resumed after its time, it fires at the next pass.

**Records and control.** `agent_triggers.source = 'agent'`, with `once` and `origin_thread_id` (migration 00025), and
`created_by` null; each run keeps `trigger_source`. The console's Triggers tab lists them with *Made by the agent* and
a link to the conversation; the team's owners and admins (a personal workspace's owner) pause, resume, run now or
delete them like any trigger, but can't change them (`409 agent_made_trigger`). Names come from the agent or from the
time (`Reminder 2026-10-21 09:00 Europe/Paris`, `Schedule 0 9 * * 1 UTC`), never from the message: names are in the
audit log, messages never are.

**Audit.** The agent's changes are `trigger.create` and `trigger.delete` with actor kind `agent` (the agent in the
entry's metadata, `agentActorId` and `agentActorName`; the console shows *Agent Planner*). Runs are recorded as every
trigger's.

## Not covered

- Exactly-once callbacks: a receiver may see a callback twice (a crash between the request and recording it);
  `X-PDP-Delivery` de-duplicates.
- A run's precise cost (above).
- Webhook senders that can't sign (most SaaS signs in its own scheme: GitHub `X-Hub-Signature-256`, Slack…):
  per-provider verification can be added as signature schemes later; until then a relay that re-signs is needed.
