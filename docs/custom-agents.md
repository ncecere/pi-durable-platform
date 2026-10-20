# Custom agents: the runtime as a library

Design note for roadmap item X, step 1 ([#28](https://github.com/ncecere/pi-durable-platform/issues/28)). Status:
**built**: the library, the facade, the test kit and the runtime contract (this note, `runtime/src/lib`,
[runtime-contract.md](runtime-contract.md)), custom state for apps ([`details` and exposed documents](#custom-state-for-apps)),
the [upgrade check](#upgrades), a [starter project](#the-starter-project) (`examples/custom-agent/`, in this
repository for now), and two first-party agents built with it, triage and a deep researcher
([first-party-agents.md](first-party-agents.md)). Running code a team wrote is step 2
([#29](https://github.com/ncecere/pi-durable-platform/issues/29)), gated on the checks listed under
[What step 2 must check](#what-step-2-must-check).

## Why

Every agent is the one program in `runtime/`: a template changes its instructions, its model and its MCP tools, never
its code. pi-durable can do much more (documents next to the transcript, hooks that guard or approve tool calls,
durable tasks, subagents, settings), but the runtime used one extension with one tool and was an application, not a
package. Someone who wants an agent with its own tools, state or guards had nothing to start from, and pi-durable is
experimental (its API changed between 1.1 and 1.2), so building on it directly means following every change.

The runtime is now a library, `@pdp/runtime`, and the platform's own agent is its default composition, built with the
same API a custom agent uses.

## Trust model

A custom agent is a runtime process built with the library. Whatever it adds runs **inside the agent's runtime**, with
everything the runtime has:

- **The agent's database role** (`AGENT_DATABASE_URL`): read and write access to the agent's whole schema, so to every
  thread's transcript, every document, and the ownership record. Not to another agent's schema (one role and schema per
  agent, see DESIGN.md, Agent storage and ownership), and not to the platform database once operators revoke PUBLIC's
  `CONNECT` there (DESIGN.md; `doctor` warns until they do).
- **The agent's gateway credential** (`PLATFORM_KEY`, `MCP_KEY`, and `MODEL_API_KEY` in proxy mode; it is the agent's
  ID and runtime token): it calls the model proxy on the team's providers and budget, the MCP proxy for the template's
  servers and the servers apps gave threads, and reports thread status and usage. It is also the runtime token the
  platform presents to the agent.
- **Direct-mode secrets** when the template uses them: a provider's API key (`MODEL_API_KEY`, `MODEL_HEADERS`) and a
  direct MCP server's URL and headers (`MCP_SERVERS`).
- **The agents' network boundary**: agent pods take traffic from the platform only and reach only their database, the
  platform's gateway and DNS (the Helm chart's NetworkPolicies), plus whatever `agents.extraEgress` allows.

What follows from that:

- **An external integration is an MCP server, not code.** A team registers it with the platform (Team MCP servers),
  which keeps its headers sealed, reaches it through the address guard, filters its tools per template and logs every
  call. Code in the agent adds logic, state and guards on top: a tool that computes, a document that remembers, a hook
  that blocks or asks before an MCP tool writes. Code that holds its own credentials for an outside system bypasses all
  of that, and the agents' egress rules refuse it by default.
- **The platform treats whatever a runtime says as untrusted input**: `/status` (read bounded, see
  [runtime-contract.md](runtime-contract.md)), thread reports, usage reports (a runtime can under-report the usage of a
  direct-mode model; proxy mode is counted by the gateway), and the transcripts it serves.
- **The library's guarantees hold for honest code.** It refuses reserved names, keeps guards in every thread's
  selection and exits when storage is lost, but code in the same process can do anything the runtime can. These checks
  catch mistakes; they are not a security boundary.
- **Today only the operator's code runs.** The runtime image is the operator's choice (`AGENTS_IMAGE`, or the local
  launcher's `AGENTS_RUNTIME_DIR`); teams can't upload code. A custom agent is therefore trusted as much as the
  operator who builds it into an image.

### What step 2 must check

Before the platform runs code a team wrote (#29), each of these needs an answer:

1. **Isolation of the pod** from the node and from other agents: a sandboxed runtime class (gVisor or Kata,
   `AGENTS_RUNTIME_CLASS`), the hardened pod the launchers already create, and resource limits per agent.
2. **Egress**: only the gateway, the agent's database and DNS, with `agents.extraEgress` closed to team code.
3. **Database access**: whether team code gets direct database credentials at all, or a storage proxy that speaks
   pi-durable's storage interface (DESIGN.md, Known limits); `REVOKE CONNECT` on the platform database a hard
   requirement, not a `doctor` warning.
4. **Secrets**: no direct-mode keys or headers for team code (proxy mode only), so the gateway counts usage and holds
   every credential.
5. **What runs**: images from a registry the platform allows, signed, recorded per agent with who deployed them, and
   audited.
6. **The contract on start**: the platform checks the runtime's contract version and refuses one it doesn't speak. (It
   already refuses to start an agent on a runtime older than the one that last wrote its data: [Upgrades](#upgrades).
   That check trusts the version the runtime reports, which is enough against an operator's rollback, not against
   code that lies.)

## The library

`runtime/` is the package `@pdp/runtime` (TypeScript sources, run with `tsx` like the runtime itself). It has two
entry points, declared in `runtime/package.json` `exports`: `@pdp/runtime` and `@pdp/runtime/testing`. Keeping it in
this repository and in the same package keeps the runtime image as it was: the image still copies `runtime/` and runs
`src/main.ts`, which is now three lines that start the default composition.

```ts
import {
	assistantExtension, defineAgent, defineDoc, defineExtension, defineTool, hook, runAgentProcess, ToolTask, Type,
} from "@pdp/runtime";

const Notes = defineDoc<{ notes: Record<string, string> }>({
	kind: "acme.notes", version: 1, scope: "conversation", history: "latest", fork: "current", initial: () => ({ notes: {} }),
});

const remember = defineTool({
	name: "remember",
	description: "Remember a note for this conversation",
	parameters: Type.Object({ text: Type.String() }),
	replay: "safe", // idempotent: keyed by the call, so a rerun after a crash writes nothing new
	execute: async ({ text }, api, context) => {
		await api.commit(async (tx) => {
			(await tx.doc(Notes, api.conversationId)).notes[api.callId] ??= text;
		}, context);
		return { output: [{ type: "text", text: "Noted." }] };
	},
});

// A guard: no MCP tool whose name says it deletes runs without a person.
const guard = hook(ToolTask, {
	beforeTool: (call) => (call.name.startsWith("mcp") && /delete/.test(call.name) ? { block: "Deleting needs a person" } : undefined),
});

await runAgentProcess(
	defineAgent({
		name: "notes",
		documents: [Notes],
		extensions: ({ instructions }) => [
			assistantExtension(instructions), // the template's instructions and get_time
			defineExtension({ name: "notes", tools: [remember], hooks: [guard] }),
		],
	}),
);
```

### API

| Export | What it is |
|---|---|
| `defineAgent(definition)` | Checks and returns an agent definition: `name`, `extensions` (an array, or a function of the agent's context: `agentId`, `name`, the template's `instructions`, `env`), `documents`, `expose` (the documents apps may read, see [Custom state for apps](#custom-state-for-apps)), `conversationCreated`, `settings` (pi-durable's settings without `extensions`, or a function of the context), `waitsForPerson` (task kinds that stand for a wait on a person's answer: they keep the agent awake neither as a busy thread nor as background work, see [hibernation.md](hibernation.md#waiting-for-a-person)), `schedules` (default true: the schedule tools when the template version allows them; false leaves them out). |
| `runAgentProcess(definition)` | Runs the definition as an agent process: from environment variables or in standby, with the exit codes and signals of the [contract](runtime-contract.md). A custom agent's entry point. It also takes a function of the agent's variables that returns a definition (`AgentChoice`, run at start or activation; an `AgentDefinitionError` from it is a misconfiguration): the platform's runtime picks a kind by `AGENT_KIND` that way. |
| `startAgent(config, definition, options)` | Starts one agent in this process and returns its HTTP handler, harness and thread store. `options` replaces storage, models or the fatal-error handler (for tests). |
| `assistantExtension(instructions)`, `assistantAgent` | The platform's own agent: the template's instructions as the prompt's preamble and `get_time`. `assistantAgent` is `defineAgent({ extensions: ({ instructions }) => [assistantExtension(instructions)] })`, what `src/main.ts` runs. |
| `approvalHook(approvals, decide)`, `AgentContext.approvals` | Tool approvals ([approvals.md](approvals.md)): `approvals.request(call, api, context, {summary?, key?})` in a `beforeTool` hook asks a person and waits, durably (across restarts and hibernation), returning `{approved, status, reason?, decidedBy?, message}`; `approvalHook` is a `ToolTask` hook that asks when `decide(call)` returns options (or `true`) and blocks a denied call. The template's marked tools (`TOOL_APPROVALS`) are asked by the library's own `pdp-approvals` extension, selected last. |
| `defineSubagent(options)` | A subagent as an extension: a replay-safe tool whose call creates a conversation, gives it the task (request ID: the call's task) and returns its answer, with `details: { conversationId }`. Options: `name`, `description`, `model`, `instructions`, `extensions` (the child's selection, computed from the caller's resolved selection), `background` (`true`, or a function deciding per call: the call returns at once, the child works in a background task, and its answer comes back to the calling conversation as a message, an entry of kind `SUBAGENT_REPORT`; `details` add `background: true` and the task; see [first-party-agents.md](first-party-agents.md#what-the-library-gained)). |
| `runAuthor(api, context)` | Who sent the message that started the run a tool call belongs to (`Author`: a person, a service account or a trigger, as the platform recorded it), or undefined. |
| `@pdp/runtime/kinds` | The first-party kinds (triage, the deep researcher) and their parts: documents, tools, prompts, settings parsers ([first-party-agents.md](first-party-agents.md)), to build a variant from. |
| The facade | `defineTool`, `defineExtension`, `section`, `hook`, `wrapTool`, `wrapSection`, `defineDoc`, `defineEntry`, `defineTask`, `configure`, `ToolTask`, `GenerationTask`, `CompactionTask`, `Type`, and the types an author needs (`Extension`, `ToolExecutionApi`, `HookApi`, `Tx`, `Context`, ...). |
| `CONTRACT_VERSION`, `PI_DURABLE_VERSION` | What `/status` reports, and the runtime reports when it starts ([Upgrades](#upgrades)). |
| `ExposedDocument`, `DEFAULT_EXPOSED_DOCUMENT_BYTES`, `MAX_EXPOSED_DOCUMENT_BYTES` | An entry of `expose` (`{ document, maxBytes }`) and its bounds (64 KiB by default, 1 MiB at most). |
| `RuntimeRefusedError` | What `startAgent` throws when the platform refuses the runtime (the upgrade check); `runAgentProcess` serves the refusal. |
| `@pdp/runtime/testing` | The [test kit](#testing). |

The rest of the main entry (`openPostgresAgentStorage`, `createRuntimeServer`, `ThreadStore`, `AgentMcp`, ...) is the
runtime's parts, exported for the platform's own runtime and for tests. They are not covered by the library's promise.

**Stable** means: within a contract version, these exports only grow; a removal or an incompatible change comes with a
new contract version and a note in the release notes. When a pi-durable upgrade changes a re-exported name or shape,
the facade adapts it where it can.

## What the library guarantees

- **It owns the extension selection.** Every thread selects the definition's extensions, in order, then the MCP
  servers' (the template's, then the thread's): when it is created, again before every message, and at start for runs a
  previous process left unfinished (before they resume). So a guard in the definition runs on every turn of every
  thread, also after the agent's model, its template's MCP servers or a thread's servers change, after the platform
  could not be reached for a thread's servers, and in threads that began before the guard existed (tested in
  `runtime/test/guards.test.ts`). The default selection, which a conversation gets when nothing chose for it (one a
  tool creates), is the definition's extensions and the template's MCP servers, never another thread's servers.
- **The schedule tools** (`schedule`, `list_schedules`, `cancel_schedule`, extension `pdp-schedules`): when the
  template version lets its agents schedule (`PLATFORM_SCHEDULES`), every thread selects them after the definition's
  extensions, unless the definition says `schedules: false`. They call the gateway with the agent's own credential;
  the platform decides what is allowed ([triggers.md](triggers.md#agent-made-schedules)).
- **Subagents keep the guards.** `defineSubagent` computes the child's selection from the caller's resolved selection
  (leaving out its own extension and the schedule tools, `pdp-schedules`: only the conversation a person or a trigger
  talks to sets schedules), never from an `{ add, remove }` edit, which would start from the host default.
- **Declared documents exist.** Each conversation document in `documents` is created in every conversation (threads,
  subagents' children, forks) in its creating commit, before `conversationCreated` runs; each session document once,
  at start.
- **Reserved names are refused** when the agent is defined or starts: extension names `mcp`, `tool-search`,
  `mcp-thread:*` and names starting with `pdp` or `pi.`; tool names starting with `mcp__` or `mcpc__` (the MCP tools'
  namespaces); document kinds and task names starting with `pi.` or `pdp.`; two extensions with one name (the second
  would replace the first); task-scoped documents and families in `documents`; `extensions` in settings.
- **Settings stay live**: a getter in `settings` is read where pi-durable uses the setting.
- **The contract**: storage in the agent's schema with single ownership, exit on lost storage, `/status`, standby and
  activation, thread and usage reports ([runtime-contract.md](runtime-contract.md)).

## What authors must not do

These are pi-durable's [API footguns](https://github.com/earendil-works/pi/blob/main/packages/durable/docs/spec.md)
that apply to agents built with the library:

- **Don't use `pi.` or `pdp.` names** for document kinds, entry kinds or task names: `pi.` belongs to pi-durable (a
  `pi.system` entry is replayed as a system message), `pdp.` to the platform (`pdp.threads`, `pdp.user`). The library
  checks document kinds and task names; it can't check the entry kinds code writes.
- **Document kinds are persisted protocol.** Never rename a kind or reuse one for another shape: a `migrate` can change
  a value, not its kind. Change the shape by raising `version` with a `migrate`. Data written by a newer version can't
  be read by older code, so rolling an agent back after it wrote is not supported ([Upgrades](#upgrades)). Extension, tool and task names are stored too (in each conversation's selection, in transcripts, in live tasks):
  keep them stable, and keep a task's definition installed while tasks of it may be pending.
- **Don't select extensions yourself.** An array passed to `configure({ extensions })` selects exactly those, and a
  guard left out runs nowhere until the library reapplies the selection at the next message. Change other fields
  (`instructions`, `thinkingLevel`, `tools`, `modelTools`) as you need; leave `model` and `extensions` to the library.
- **Only an idempotent tool is `replay: "safe"`.** A tool's intent is committed before it runs; after a crash a
  replay-safe tool runs again, any other ends with an `interrupted` result. A replay-safe tool with effects must find
  its earlier attempt: key the effect by `api.callId` (or `api.taskId`) and record it in storage, or pass the same
  request ID to the outside system. Nested calls (`api.executeTool`) reattach by their key only in a replay-safe
  caller.
- **Never await the outside world inside a commit** (a model, a tool, the network, a person, another commit), and
  read through `tx` there: other reads wait on the commit itself.
- **Prefix memo names** in hooks: they share the task's memo namespace.
- **Don't report a subagent's usage** in the tool's result: the child's conversation already counts it.
- **Keep prompt sections stable**: a section that renders the time or a counter adds system entries and misses the
  provider's prompt cache on every request.
- **Honour the abort signal** (`context.abortSignal`) in tools and hooks: hibernation waits for running code to return.
- **Background work keeps the agent awake, up to a ceiling** (`AGENTS_BACKGROUND_MAX`, 2 hours by default): past it
  the platform aborts it (abort handlers run) and the agent may hibernate. Split long jobs into tasks that each finish,
  and declare tasks that wait for a person in `waitsForPerson` ([hibernation.md](hibernation.md)).
- **Never log secrets** from the agent's context (`env` holds the model key, the gateway credential and MCP headers),
  and never send them anywhere but where the platform meant them to go.
- **Import the library, not pi-durable.** What the facade leaves out is not covered by the library's promise.

## Custom state for apps

An agent's state reaches the apps that use it in two ways, both through the platform and with exactly the access
reading the thread needs (another's private thread is not found; platform roles never reach it, but a platform admin's
reasoned, audited read):

- **`details` on tool results.** A tool's `details` (pi-durable's channel for UIs: the result's `details`, or the last
  `api.details()` while it runs) appear on its result message in the thread view (`details`) and on the current
  round's tool (`tools[].details`), in both event formats (`message.added`, `tool.updated`/`tool.finished` with
  `details`). JSON only, at most 16 KiB each: larger ones are left out and `detailsOmitted` says so. The console's chat
  shows them, raw, in the tool call's card. The model never sees them.
- **Exposed documents.** A conversation document listed in `documents` can be exposed, opt-in and bounded:

  ```ts
  defineAgent({ documents: [Board], expose: [{ document: Board, maxBytes: 32 * 1024 }] }); // or expose: [Board] (64 KiB)
  ```

  Apps read it with `GET /v1/agents/{agent}/threads/{thread}/documents/{kind}`: `{kind, version, revision, value}`,
  where `version` is the definition's (values are migrated to it when read) and `revision` a hash of its content. A
  kind the agent doesn't expose, or the thread doesn't have, is `404 document_not_found` (a pi-durable or platform
  document, `pi.*` and `pdp.*`, can never be exposed); one larger than its bound is `422 document_too_large`, never cut.
  The thread's view lists its exposed documents with their revisions (`documents: {kind: {revision}}`, in whole views
  too), and the delta stream sends `document.updated` (`kind`, `revision`) when one changes; the event never carries
  the document. Platform admins read one with a reason (`POST /v1/admin/agents/{agent}/threads/{thread}/documents/{kind}/read`),
  recorded as `thread.admin_read` with the document's kind and the outcome (`read`, `not_found`, `failed`): every
  attempt is recorded, and a missing thread or kind is the same `404 document_not_found`, so staff learn nothing about
  which exist.

**Session documents are not exposed.** A session document is one per agent, shared by every thread: reading it through
a thread would show other threads' state, private ones included, to anyone who can read that thread, and reading it
outside a thread needs an access rule threads don't give (who may read an agent's state across its conversations). An
author who wants per-thread state for apps keeps it in a conversation document; an agent-wide view for its team (the
triage queue, #31) is a later design, with its own rule.

## Upgrades

pi-durable migrates a document when it reads it, and code that finds a document a newer version wrote refuses it. So
once a runtime with a newer pi-durable has written an agent's storage, an older runtime can't run the agent: its
resumed runs and its tools would fail on every read. The platform checks:

- The runtime reports its contract and pi-durable versions when it starts, **before it opens the agent's storage**
  (`POST {PLATFORM_GATEWAY_URL}/runtime/start`), and at `/status`.
- The platform records, per agent, the newest pi-durable version that has written its storage
  (`agent_storage_versions`), and refuses a runtime older than that: the gateway answers `409 runtime_too_old`, and the
  runtime never opens the storage and answers every request with the refusal; the platform also checks `/status`
  after every start (for a runtime that didn't ask) and stops a refused runtime. Requests that wake the agent answer
  `409 runtime_too_old` with the reason, the agent (`runtimeRefused`, `storageVersion`) and its page say why, and the
  first refusal of a version is audited (`agent.runtime_refused`). A runtime that reports no version counts as older.
- A runtime as new or newer starts, and a newer one moves the record up (`agent.runtime_recorded`).
- **What a runtime reports is not trusted beyond the platform's own pi-durable.** The runtime's key is readable by the
  code its tools run, so a tool could report `999999.0.0` and make the platform refuse every genuine runtime. The
  platform knows the pi-durable it ships (the pin in `runtime/package.json`, `agents.ShippedPiDurableVersion`; a test
  fails when they differ) and records a reported version only when it has the same major and is at most one minor
  version ahead (with 1.3.0 shipped: up to 1.4.x), so a custom image that upgrades pi-durable a release early works. A
  version beyond that is not recorded and changes nothing (the record, a refusal): it is not older than any recorded
  version, so that runtime starts, and the platform logs a warning with the agent and the reported version. Running a
  pi-durable that far ahead is unsupported: the upgrade check doesn't protect an agent whose data it wrote.
- **Resetting the record.** A version within that tolerance that no runtime really wrote (a tool reporting `1.4.9`),
  or one recorded before this check, still refuses genuine runtimes. An operator removes the record with
  `pi-durable-platform reset-storage-version --agent <id>` (the next runtime start records its own version), or sets it
  with `--to <version>`; it ends any refusal and is audited in the agent's team's log
  (`agent.storage_version_reset`, system actor, with the old and new versions). Don't reset below a version that has
  really written the agent's data: an older runtime would then fail on it.

**Rolling a runtime back after it has written an agent's data is not supported.** To undo an upgrade, fix forward:
deploy a runtime with the newer pi-durable again. Before upgrading pi-durable, test the agent on a copy of its data;
the record moves at the first start of the new runtime, before it writes anything.

## The starter project

[`examples/custom-agent/`](../examples/custom-agent/) is a starter agent (a task board on the assistant): one tool with
details, one exposed document, one guard (a `beforeTool` hook that blocks a dangerous call), its tests with the test
kit (a conversation against the fake model, restarts in the middle of a tool call and of the model's answer, the
runtime conformance suite), a Dockerfile on the runtime image, and the local loop: the local launcher's
`AGENTS_RUNTIME_DIR` pointed at it, the compose PostgreSQL, and the console's Test tab. It is in this repository, linked
to `runtime/`, until the library is published; CI builds and tests it. For larger examples, the first-party kinds in
`runtime/src/kinds/` ([first-party-agents.md](first-party-agents.md#building-a-variant)) use only the library's public
exports too.

## Versioning

- **The library's version** is the runtime's (`runtime/package.json`, the platform's release version).
- **The contract version** (`CONTRACT_VERSION`, 2 since background work at `/status`) is separate and reported at `/status`. It goes up only when a
  runtime and a platform of different contract versions can't work together; additions both sides may ignore keep it.
  The platform accepts the field (bounded) and stores nothing from it yet.
- **pi-durable is pinned** (`1.3.0`, exact in `package.json`; `PI_DURABLE_VERSION` at `/status`, checked against the
  installed package by a test; the platform's `agents.ShippedPiDurableVersion`, checked against the pin by a test). An upgrade runs pi-durable's storage conformance suite on PostgreSQL, the restart tests
  and the runtime conformance suite, and the facade absorbs API changes. It is a one-way step for every agent the new
  runtime starts ([Upgrades](#upgrades)).

## Testing

`@pdp/runtime/testing` is runner-agnostic (the suites take `describe`, `it`, `expect`, `beforeAll` and `afterAll`), and
the platform's own runtime tests use it:

- `fakeModel()`: a scripted model on pi-ai's faux provider (`reply(...)` queues answers, `toolCall(...)` and `answer(...)`
  build responses; with nothing queued it answers "ok"). `gate()` and `afterGate(gate, response)` stop a tool, a model
  request or a subagent at a point the test controls.
- `memoryStorage()`: in-memory storage that survives a simulated crash.
- `startTestAgent(definition, options)`: the agent on in-memory storage and a fake model, with its HTTP API on a loopback
  port; `send`, `view`, `settled`, `status`, `document` (an exposed document, as apps read it), and `restart()`, which crashes the process wherever it is (nothing more
  reaches storage, running code is aborted) and starts it again on the same storage.
- `agent.approvals(threadId?)`, `agent.pendingApproval(threadId?)` and `agent.decide(id, "approve" | "deny", body?)`:
  tool approvals as the platform would decide them (`POST /approvals/{id}`); without a platform the runtime denies
  a request itself when it expires. `restart({ graceful: true })` stops the agent as hibernation does.
- `expectReplayed(agent, threadId)`: after any number of restarts, every tool call has exactly one result, none was
  interrupted (unless `allowInterrupted`), the run ended with an answer, and every subagent was given its task once and
  its answer reached the parent (a background subagent's exactly once, as a message).
- `registerRuntimeConformance` and `registerStandbyConformance`: the [runtime contract](runtime-contract.md) as HTTP
  tests, against any runtime.
- `registerStorageConformance`: pi-durable's storage conformance suite, for an alternative storage.

```ts
const inTool = gate();             // the test's "remember" tool awaits inTool.wait(context) first
const model = fakeModel();
model.reply(toolCall("remember", { text: "milk" }, { id: "r1" }), "Noted.");
const first = await startTestAgent(definition, { model });
await first.send("t", "remember milk");
await inTool.reached;               // the tool is running
const second = await first.restart(); // crash, then start again on the same storage
inTool.open();
await second.settled("t");
await expectReplayed(second, "t");  // one result for r1, nothing lost or repeated
```
