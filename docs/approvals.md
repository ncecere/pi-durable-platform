# Tool approvals: a person approves sensitive tool calls before they run

Design note for roadmap item P ([#16](https://github.com/ncecere/pi-durable-platform/issues/16)). Status: **built**
(this note, `runtime/src/lib/approvals.ts`, `internal/agents/approvals.go`, migration 00024, the console's test chat
and Approvals tab).

## Why

A template can give an agent a tool or not; there was no "ask first". An agent that may create issues, send mail or
delete records through an MCP server either has the tool and uses it whenever the model decides, or doesn't have it.
Agents that run on their own (triggers, #15) make this sharper: nobody is watching the conversation when the call is
made.

## What a template version says

A version has `approvals`:

```json
{"tools": ["mcp__github__create_issue", "mcp__mail__*"], "role": "admin", "timeoutSeconds": 86400}
```

- `tools`: tool names, or patterns where `*` stands for any characters (built-in tools such as `get_time`, template MCP
  tools `mcp__<slug>__<tool>`, apps' thread tools `mcpc__<slug>__<tool>`). At most 100 patterns of 1-200 of
  `[A-Za-z0-9_.*-]`. Empty: no approvals.
- `role`: the lowest team role that may decide calls in shared threads besides the person the run is for: `admin`
  (default: owners and admins), `editor` or `member`. See [Who may decide](#who-may-decide).
- `timeoutSeconds`: how long a request waits for a decision before it is denied (60 s to 30 days, default 24 h).

It is part of the version like the instructions: a new version (omitted keeps the current version's), upgrades and
rollbacks change it with the version, and the runtime gets it at start (`TOOL_APPROVALS`, JSON `{tools,
timeoutSeconds}`; activation accepts it too). The console's template editor (new version) edits it.

## Where the wait lives, and why it survives hibernation

The library installs an extension of its own, `pdp-approvals`, last in every thread's selection (after the definition's
extensions and the MCP servers'), with one `beforeTool` hook on pi-durable's `ToolTask`. For a call whose tool matches,
the hook asks for approval and awaits the answer; on approval it returns nothing (the call runs), on denial it blocks
the call with a reason, which pi-durable turns into the call's error result (`Tool call blocked: denied by Ann: not
today`), so the model sees the refusal and goes on.

Why this is durable: pi-durable runs `beforeTool` in the tool task's first phase, `call`, **before it records the
call's intent**. Nothing of the tool has run; the task's checkpoint is still `{phase: "call"}`. When the process stops
while the hook waits (hibernation: `SIGTERM`, the harness closes and aborts running invocations; or a crash), the
hook's abort signal fires, the scheduler ends the invocation without writing (a closing harness writes nothing) and the
task stays live in storage. The next process resumes it from `call`: it resolves the tool again, runs the hooks again,
and the approval hook finds its request (its ID is derived from the tool task's ID, which is durable) in the agent's
storage, with the decision if one has arrived meanwhile. So the wait costs no running code across a restart, and a
decision made while the agent is hibernated is applied when it next starts.

*Not chosen:* a child task in pi-durable's `waiting` state. That is how a task waits for other tasks without running
code, but `ToolTask` is pi-durable's built-in task: its phases can't be changed to wait, and a hook can't commit a
`waiting` state (hooks get read-only access and memos). Wrapping the tool instead (`wrapTool`) would wait inside
`execute`, *after* the intent is recorded: a restart would then end a tool that isn't replay-safe with `interrupted`
instead of asking again. Waiting in `beforeTool` needs no change to pi-durable and is replay-safe for every tool.

**The request's state** is a session document, `pdp.approvals` (version 1), that the library owns: per approval its ID,
thread, conversation, tool task and call, the tool's name, a redacted view of the arguments, the reason an author gave,
who the run is for, when it was asked, and the outcome (`pending`, `approved`, `denied`, `timed_out`, `cancelled`) with
who decided, when and why. The hook commits the request before reporting it, so a request the platform knows is always
in storage; decisions are committed by the runtime's route that receives them. Resolved entries are pruned (the oldest
beyond 500).

**Cancelled.** A run stopped while it waits (`POST …/stop`, deleting the thread) aborts the tool task; the request
is then `cancelled` (checked after the stop, and at every start for requests whose task is gone), and reported.

### The platform's side

1. **Request.** The runtime reports each new request to the gateway (`POST /approvals/report`, the agent's gateway
   credential) with the tool, the redacted arguments, the thread, who the run is for, and the author's reason. The
   platform keeps it (`tool_approvals`), computes when it expires from the agent's version (not from the runtime), and
   answers with the request's state (a request it already decided, after a restart, is answered at once). Audited
   `tool_approval.request`.
2. **Decision.** `POST /v1/agents/{id}/approvals/{approvalId} {decision, reason?}` records the decision (audited
   `tool_approval.decide`, in the same transaction), then delivers it to the runtime: `POST /approvals/{id}` on the
   runtime, **waking the agent if it is hibernated**. Delivery happens after the answer to the person (a cold start may
   take a while) and is retried by the platform's approvals loop until it lands. The runtime commits the decision; the
   waiting hook (or the hook rerun by the next start) applies it.
3. **Timeout.** The approvals loop (every replica, every 30 s; each row changes once) marks pending requests past
   their expiry `timed_out` (audited `tool_approval.timeout`) and delivers that like a decision: the call is denied.
4. **At start** the runtime sends its pending requests (`POST /approvals/sync`); the platform records any it missed,
   answers with the decisions it has for them, and cancels pending requests the runtime no longer has (older than the
   runtime's list).

The runtime is untrusted, as for thread reports: the platform bounds and redacts the arguments again, ignores a thread
or person that doesn't parse, and a runtime that names someone as the run's person only lets them decide calls in
threads they could already read.

## Who may decide

Whoever may see an approval may see the thread: the agent's users (people and service accounts of its team; for a
service-managed agent, its team's service accounts) who can read the thread (a private thread: its creator). Of them:

| Thread | May decide |
|---|---|
| Private | Its creator only. |
| Shared, a person's or service account's run | The one whose message started the run; the team's owners and admins; members whose role is at least the version's `role`. |
| Unattended (no author: triggers) | The team's owners and admins (editor service accounts of a service-managed agent). |

For a service-managed agent the team's people never see its conversations, so its editor service accounts act as its
admins. **Staff can't decide**: platform admins and auditors don't use agents of teams they aren't on (404), and the
reasoned read doesn't extend to approvals. Someone who may see but not decide gets `canDecide: false` and 403
`approval_forbidden` when they try.

**Who the run is for** is the author of the user message that started the run: the runtime walks back from the call's
reply through the transcript to the first message after the previous answer (a steer that joined the run doesn't change
it). For a call in a subagent's conversation, the thread's. A run with no author (none recorded, or a trigger) is
unattended.

## The API

| Route | |
|---|---|
| `GET /v1/agents/{id}/approvals?thread=&status=pending\|decided\|all&limit=` | An agent's approvals the caller may see, newest first (default pending). |
| `GET /v1/agents/{id}/approvals/{approvalId}` | One. |
| `POST /v1/agents/{id}/approvals/{approvalId}` | `{decision: approve\|deny, reason?}` (reason at most 1000 characters, given to the model on denial). 409 `approval_decided` once decided, timed out or cancelled. |
| `GET /v1/me/approvals` | The inbox: pending approvals the caller may decide, across agents. |

A `ToolApproval` is `{id, agentId, agentName, threadId, tool, callId, arguments, summary?, requestedFor?, status,
requestedAt, expiresAt, decidedAt?, decidedBy?, reason?, canDecide}`. `arguments` is a redacted view: values under keys
that look like credentials (`authorization`, `cookie`, `token`, `secret`, `password`, `api_key`, `headers`, ...) read
`[redacted]`, strings are cut at 2,000 characters, nesting at 6 levels, and the whole view at 16 KiB. Audit entries
never hold arguments: the tool, the thread, the decision and its reason.

## The thread view and its streams

The runtime's chat view (`GET …/threads/{thread}` and both stream formats) shows approvals:

- each tool call in the transcript carries `approval` (`{id, status, requestedAt, expiresAt?, decidedAt?, decidedBy?,
  reason?, summary?, requestedFor?}`) once one was asked for it, so the pending state and then the decision appear on
  the call, before its result message;
- `approvals`: the thread's pending requests (with the redacted arguments), absent when there are none, including
  calls in a subagent's conversation.

In the delta format, a call's approval changing is a `message.updated` of its reply, and the pending list an
`approvals.set` event (the whole list; `[]` clears it). A whole-view stream sends a view after either. Clients of the
delta format that ignore unknown events keep working; the event is an addition within version 1.

## Hibernation while waiting

`/status` gains `pendingApprovals` (requests waiting for a person) and `waitingThreads`: busy threads whose reply waits
only for a person (a pending approval, no model request streaming and no tool running). They are also counted in
`busyThreads`. The reaper (#34) may treat them as idle: hibernating then is safe, since the wait resumes at the next
start and a decision wakes the agent.

## Hooks written with the library

The agent's context has `approvals`, so an author's hook can ask too:

```ts
defineAgent({
	extensions: ({ approvals }) => [
		defineExtension({
			name: "refunds",
			tools: [refund],
			hooks: [
				approvalHook(approvals, (call) =>
					call.name === "refund" && (call.arguments.amount as number) > 100 ? { summary: "Refund over 100 EUR" } : undefined,
				),
			],
		}),
	],
});
```

`approvals.request(call, api, context, {summary?, key?})` returns `{approved, status, reason?, decidedBy?}`;
`approvalHook(approvals, decide)` is a `ToolTask` hook that asks when `decide` returns options (or `true`) and blocks a
denied call. Two asks for one call with the same `key` (default `""`) are one request, so a tool both an author's hook
and the template mark is asked once. Without a platform (the test kit), the runtime decides time-outs itself and tests
decide through `POST /approvals/{id}`.

## Limits

- One decision per call: there is no "approve for this thread" or "always". A later item can add standing approvals.
- Approvals aren't announced on `GET /v1/agents/events`; the inbox is polled (the console every 15 s), and the thread's
  own stream shows them live.
- A decision for an agent that was turned off waits until it is turned on (delivery retries; the request may time out
  meanwhile).
