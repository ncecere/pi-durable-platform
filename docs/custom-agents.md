# Custom agents: the runtime as a library

Design note for roadmap item X, step 1 ([#28](https://github.com/ncecere/pi-durable-platform/issues/28)). Status:
**the library, the facade, the test kit and the runtime contract are built** (this note, `runtime/src/lib`,
[runtime-contract.md](runtime-contract.md)). Still to come in #28: the documents endpoint and `details` for apps, the
platform's upgrade check, and the starter repository. Running code a team wrote is step 2
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
6. **The contract on start**: the platform checks the runtime's contract version and refuses one it doesn't speak, and
   refuses to start an agent on a runtime older than the one that last wrote its data (the upgrade check, later in
   #28).

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
| `defineAgent(definition)` | Checks and returns an agent definition: `name`, `extensions` (an array, or a function of the agent's context: `agentId`, `name`, the template's `instructions`, `env`), `documents`, `conversationCreated`, `settings` (pi-durable's settings without `extensions`, or a function of the context). |
| `runAgentProcess(definition)` | Runs the definition as an agent process: from environment variables or in standby, with the exit codes and signals of the [contract](runtime-contract.md). A custom agent's entry point. |
| `startAgent(config, definition, options)` | Starts one agent in this process and returns its HTTP handler, harness and thread store. `options` replaces storage, models or the fatal-error handler (for tests). |
| `assistantExtension(instructions)`, `assistantAgent` | The platform's own agent: the template's instructions as the prompt's preamble and `get_time`. `assistantAgent` is `defineAgent({ extensions: ({ instructions }) => [assistantExtension(instructions)] })`, what `src/main.ts` runs. |
| `defineSubagent(options)` | A foreground subagent as an extension: a replay-safe tool whose call creates a conversation it owns, gives it the task (request ID: the call's task) and returns its answer, with `details: { conversationId }`. Options: `name`, `description`, `model`, `instructions`, `extensions` (the child's selection, computed from the caller's resolved selection). |
| The facade | `defineTool`, `defineExtension`, `section`, `hook`, `wrapTool`, `wrapSection`, `defineDoc`, `defineEntry`, `defineTask`, `configure`, `ToolTask`, `GenerationTask`, `CompactionTask`, `Type`, and the types an author needs (`Extension`, `ToolExecutionApi`, `HookApi`, `Tx`, `Context`, ...). |
| `CONTRACT_VERSION`, `PI_DURABLE_VERSION` | What `/status` reports. |
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
- **Subagents keep the guards.** `defineSubagent` computes the child's selection from the caller's resolved selection
  (leaving out only its own extension), never from an `{ add, remove }` edit, which would start from the host default.
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
  be read by older code, so rolling an agent back after it wrote is not supported (the platform's check comes later in
  #28). Extension, tool and task names are stored too (in each conversation's selection, in transcripts, in live tasks):
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
- **Never log secrets** from the agent's context (`env` holds the model key, the gateway credential and MCP headers),
  and never send them anywhere but where the platform meant them to go.
- **Import the library, not pi-durable.** What the facade leaves out is not covered by the library's promise.

## Versioning

- **The library's version** is the runtime's (`runtime/package.json`, the platform's release version).
- **The contract version** (`CONTRACT_VERSION`, 1) is separate and reported at `/status`. It goes up only when a
  runtime and a platform of different contract versions can't work together; additions both sides may ignore keep it.
  The platform accepts the field (bounded) and stores nothing yet.
- **pi-durable is pinned** (`1.3.0`, exact in `package.json`; `PI_DURABLE_VERSION` at `/status`, checked against the
  installed package by a test). An upgrade runs pi-durable's storage conformance suite on PostgreSQL, the restart tests
  and the runtime conformance suite, and the facade absorbs API changes.

## Testing

`@pdp/runtime/testing` is runner-agnostic (the suites take `describe`, `it`, `expect`, `beforeAll` and `afterAll`), and
the platform's own runtime tests use it:

- `fakeModel()`: a scripted model on pi-ai's faux provider (`reply(...)` queues answers, `toolCall(...)` and `answer(...)`
  build responses; with nothing queued it answers "ok"). `gate()` and `afterGate(gate, response)` stop a tool, a model
  request or a subagent at a point the test controls.
- `memoryStorage()`: in-memory storage that survives a simulated crash.
- `startTestAgent(definition, options)`: the agent on in-memory storage and a fake model, with its HTTP API on a loopback
  port; `send`, `view`, `settled`, `status`, and `restart()`, which crashes the process wherever it is (nothing more
  reaches storage, running code is aborted) and starts it again on the same storage.
- `expectReplayed(agent, threadId)`: after any number of restarts, every tool call has exactly one result, none was
  interrupted (unless `allowInterrupted`), the run ended with an answer, and every subagent was given its task once and
  its answer reached the parent.
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
