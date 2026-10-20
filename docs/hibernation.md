# Background-aware hibernation

Design note for roadmap item Z1 ([#34](https://github.com/ncecere/pi-durable-platform/issues/34)). Until v0.3 the idle
reaper hibernated an agent when its runtime said no thread had a reply in progress, no event stream was open and nothing
had happened for `AGENTS_IDLE_TIMEOUT`. Work that is not a thread's reply (a background task, a background subagent and
its conversation, a tool call still running after its reply ended) didn't count, so an agent could be hibernated in the
middle of it. Triggers (#15) and agents built with the library (#28) make such work normal.

## What counts as live work

The runtime reads it from pi-durable 1.3.0, which has no host wake-up API: `harness.inspect()` lists every live task
(pending, running, waiting, completing) with its record (`startedAt`, owner, `background`, kind) and whether the
scheduler can run it, and `harness.taskGraph()` says which conversations each live task owns
(`runtime/src/lib/background.ts`). Both are reads: nothing is written and no task code runs.

Following a task's owners up (its owner task, or the live task that owns its conversation):

- **A thread's ordinary run**: no task on the way is a background task, and the top one is in a thread whose reply is in
  progress (`pi.live.run`). This is what `busyThreads` has always counted.
- **Background work**: everything else, in units. A unit is the nearest background task (`background: true`) above a
  task, or else the task at the top: a task in a conversation that isn't a thread (a background subagent's run, once the
  anchor task that created its conversation has ended; a conversation a custom agent created), or a task left in a
  thread whose reply has ended (a tool call still running). A unit's start is its top task's `startedAt`, or, for one that
  has not run yet, when the runtime first saw it.
- **Idle work** keeps nothing awake: a task no definition can run (`blocked` in `inspect()`), a task that waits for a
  person (below), and a task whose every dependency (the tasks it waits `on` and the work it owns) is idle, at least
  one: the background job waiting on a question, the tool call awaiting an approval, the reply waiting on that tool call.
  A thread whose reply only waits for a person is not a busy thread.

## `/status` (contract version 2)

```json
"busyThreads": 0, "openStreams": 0, "lastActivityAt": 1760000000000,
"backgroundWork": {"tasks": 1, "oldestStartedAt": 1759990000000, "waitingForPerson": 0}
```

`tasks` counts the units of background work that are not idle (at most 1000), `oldestStartedAt` is the oldest one's
start (Unix ms; `null` with none) and `waitingForPerson` the live tasks of a kind that waits for a person (at most 1000;
they are in neither count). Background work running doesn't touch `lastActivityAt`; a unit that ends does (the idle
clock starts then), unless the platform aborted it for its age.

`POST /background/abort {"olderThanMs": n}` aborts every unit that is not idle and started at least `n` ms ago, with
everything it owns (`harness.abortTask`, so abort handlers run), waits up to 5 seconds for it to end and answers
`{"aborted": count, "settled": bool}`. It never aborts a thread's reply in progress and doesn't count as activity.

The platform reads `backgroundWork` strictly bounded (`internal/agenthost`): `tasks` and `waitingForPerson` are whole
numbers from 0, clamped to 1000; `oldestStartedAt` is required when `tasks` is above 0 and must be a plausible Unix ms
time. Anything else drops the field, and a runtime that doesn't report it (contract version 1, an agent started before
the upgrade) is judged as before.

## The reaper

The reaper keeps an agent awake while its runtime reports a busy thread, an open stream, background work younger than
the ceiling, or activity within the idle timeout. A runtime that reports no `openStreams` is judged by its own
replica's streams, and one that reports no `backgroundWork` by the old rule, until it restarts. While background work
keeps an agent awake, the reaper asks again after half an idle timeout, or when the oldest unit reaches the ceiling if
that comes first.

**The ceiling.** Background work older than `AGENTS_BACKGROUND_MAX` (default `2h`, from 1 minute to 7 days) no longer
keeps the agent awake: a stuck task can't hold a runtime forever. When it is the only thing keeping an agent awake (no
busy thread, no open stream, no recent activity, no younger background work), the reaper calls
`POST /background/abort` with the ceiling, records an audit entry on the agent's team (`agent.background_abort`, actor
system, with the count aborted and the ceiling), counts it (`agent_background_aborts_total`), reads `/status` again and
hibernates the agent if nothing else keeps it awake. Younger background work that remains keeps it awake, and is
measured on its own. The ceiling is one platform setting applied to each agent's own work; the per-agent limits of
triggers (#15) may replace it for the agents they start.

Several replicas: everything is the runtime's report, so the rule doesn't depend on which replica reaps (one at a time,
as before). A ceiling abort is idempotent: a second one finds nothing old left.

## Waiting for a person

**Rule: a task waiting for a person's answer counts as idle.** An approval (#16) or a question doesn't hold an agent
awake: the agent may hibernate while the person thinks, and the work resumes when the answer arrives and wakes it.

**The seam for #16** is a marker on task kinds. The library treats a live task of a marked kind as a wait for a person:

- `AgentDefinition.waitsForPerson`: task kinds (`defineTask` names) a custom agent declares, for its own questions.
- `PLATFORM_PERSON_WAIT_KINDS` in `runtime/src/lib/background.ts`: the platform's own (empty today). The approvals work
  adds its approval task's kind (a `pdp.` name) there.

What the marked task and the work waiting on it must do, so that hibernation in the middle loses nothing:

- **The wait is durable.** The answer is in storage (the approval record, a document), never only in memory, and the
  task that waits for it is live in storage while it waits. Hibernation is a graceful stop: running code is aborted,
  and the next start resumes from storage.
- **What waits on it can be resumed.** A task waits with a committed `waiting` state (`on: [question]`), which survives
  a restart as it is. A tool call that awaits the question in memory (`api.waitForTask`) must be `replay: "safe"` and
  find its question again on the rerun (by a memo or a key, as `defineSubagent` finds its child), or after a restart it
  ends `interrupted`.
- **Nothing else runs meanwhile.** A task counts as idle only when everything it owns and waits on is: it must not do
  other work in parallel with the wait, which hibernation would interrupt.
- **The answer wakes the agent.** The platform delivers it with a request to the runtime (a route #16 adds, through
  `agenthost.Proxy`, which wakes the agent). In 1.3.0 the runtime then completes the marked task (or writes the answer
  where the waiting task reads it), and the work goes on.

The marked kinds are reported as `waitingForPerson` at `/status`, so the console can say an agent is waiting for
someone rather than working.

## Visibility

- **Metrics**: `agents_awake{reason}` (reply, stream, background, recent): running agents the reaper found awake for
  each reason in its last pass (an agent can count for several); `agent_background_aborts_total{result}`.
- **The agent page**: `GET /v1/agents/{agentId}` carries `awake` while the agent runs: why it isn't hibernating
  (`reasons`), and its background work (count, the oldest start, the tasks waiting for a person). The platform asks the
  runtime's `/status` for it (at most every 5 seconds per agent, 2 seconds' timeout; absent when the runtime doesn't
  answer). The list of agents doesn't carry it (one request per agent).

## When pi-durable ships a wake-up API

A newer, unreleased pi-durable adds a host wake-up API (`harness.wake()` and `onWake`). With it:

- A task waiting for a person parks with no code running and no in-memory await, and the host wakes it with the answer
  (`harness.wake`). The runtime then tells such waits from work by pi-durable's own state instead of the kind marker,
  the approvals work drops the replay-safe rerun above, and the answer route becomes a wake call. The marker stays
  for agents that use it until the contract version after that.
- A task that waits for a time (a retry backoff, a scheduled step, triggers' schedules in #15) can say when it wants to
  run next. The runtime would report the earliest such time (`nextWakeAt`), the reaper would hibernate an agent whose
  work only waits for a time, and the platform would wake it then: the known limit "scheduled work while an agent
  hibernates" in DESIGN.md closes with it.
- Background work would still be counted as here; only the "waiting" part moves from a convention to the API.

Each of these changes `/status` or adds a route: a new contract version, and the platform keeps reading version 2.

## Not covered

- A unit is measured from its top task's start: a background subagent that keeps working through a chain of runs, each
  started afresh, is measured per run, not as a whole. Its reporter tasks (one per message) are measured as units.
- Clocks: the reaper compares the runtime's `oldestStartedAt` with its own clock, as it does `lastActivityAt`; the
  abort takes an age, measured on the runtime's clock, so a skew delays an abort rather than aborting young work.
- Shell commands a tool starts are counted while their tool call is live; a process a tool leaves running after its
  call ended is outside pi-durable and not seen.
