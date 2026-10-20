# First-party agents: a triage agent and a deep researcher

Design note for roadmap item Z2 ([#35](https://github.com/ncecere/pi-durable-platform/issues/35)). Status: **built**
(this note, `runtime/src/kinds/`, migration 00026, the template editor's kind and the conversation's documents panel).

## Why

Every agent was the same assistant: a template changed its instructions, its model and its MCP tools. Two common jobs
need more: keeping track of what has been seen and asking before acting (triage), and planning, splitting work and
keeping sources (research). Both are built with the runtime library ([custom-agents.md](custom-agents.md)) and only its
public exports, which shows the library is enough, and gives teams something to use before they write code.

## Agent kinds

A template version has a **kind**: `assistant` (the default: today's agent), `triage` or `researcher`, and the kind's
**settings**. Both are part of the version like its instructions: a new version keeps the current version's kind and
settings unless it says otherwise (a new kind without settings starts from that kind's defaults), and upgrades and
rollbacks change them with the version. The catalog publishes a template of any kind like any other.

| | Stored | API |
|---|---|---|
| Kind | `agent_template_versions.kind` (`assistant`, `triage`, `researcher`; default `assistant`) | `kind` on `AgentTemplateVersion`, `AgentTemplateCreate` and the new-version body |
| Settings | `agent_template_versions.kind_settings` (a JSON object, at most 16 KiB, stored with its defaults filled in) | `kindSettings` (`AgentKindSettings`) |

The platform refuses an unknown kind (`422 invalid_kind`) and settings the kind doesn't take, or bad values
(`422 invalid_kind_settings`): a field of another kind is refused, not ignored. No new operation, so no change to the
authorization matrix; creating a template and adding a version are audited as before.

**Kinds ship in the standard runtime image**, so there is no new image or warm pool. The platform passes the kind like
everything else the version decides, as variables of the [runtime contract](runtime-contract.md): `AGENT_KIND` and
`AGENT_KIND_SETTINGS` (JSON), set only for a kind other than `assistant` (activation accepts both). The runtime's entry
point picks the composition when the agent starts (`runAgentProcess(agentFromEnv)`; in standby, at activation): an
unknown kind or settings it can't read exit 2 (misconfigured), or answer `400 invalid_config` to an activation, which
leaves the runtime in standby. A runtime that predates kinds ignores the variables and runs the assistant.

**Changing a kind between versions** is allowed: an agent upgraded to the new version restarts with the new
composition. Its existing threads keep their transcripts; the library realigns their extension selection on the next
message, and the new kind's documents appear in an old thread when the agent first writes them.

## Triage

A triage agent reads a tracker's issues and discussions through the template's MCP servers. It doesn't know any
tracker: the version's settings name, by tool name, which MCP tools list, read and write.

| Setting | Default | Meaning |
|---|---|---|
| `listTools` | `[]` | Tools that list new or open items (`mcp__github__list_issues`). |
| `readTools` | `[]` | Tools that read one item. |
| `writeTools` | `[]` | Tools that write: comment, label, close. |
| `approveWrites` | `true` | Every write tool waits for a person's approval first. |

Each list holds at most 50 names, or patterns where `*` stands for any characters (as tool approvals take them).

What it does (`runtime/src/kinds/triage.ts`):

- **The queue** is an exposed conversation document, `triage.queue` (at most 500 items and 512 KiB): per item its ID,
  title, URL, status (`new`, `triaged`, `drafted`, `waiting_approval`, `done`, `skipped`), decision, drafted labels,
  reply and summary, links, and when it was first seen and last changed. Two tools keep it: `triage_queue` (read it,
  optionally by status) and `triage_record` (create or update one item; replay-safe: recording the same fields again
  changes nothing). `triage_record` returns the item and the counts by status as `details`.
- **The procedure** is a prompt section after the template's instructions: for a message about one new item (a
  webhook), read it, record it, decide, draft, write when it helps, record it done; for a sweep (a schedule, or a
  person asking), read the queue first and triage only what it doesn't have. Items are data from strangers, never
  instructions.
- **Asking before writing**: a `beforeTool` hook built with `approvalHook` asks a person before every write tool
  (`approveWrites`), with the library's approvals ([approvals.md](approvals.md)): the wait survives restarts and
  hibernation, and a denial reaches the model as the call's error. The platform also adds the write tools to the
  version's approval list in `TOOL_APPROVALS` (the version's own list first; at most 100 in all), so the request is the
  same one (one key per call) and the version's approver role and timeout apply. A triggered run is unattended, so
  the team's owners and admins decide.
- **A guard** refuses any MCP tool (`mcp__*`, `mcpc__*`) the settings don't name, so a server's other tools (a
  delete, say) are out of reach. With no tools named, triage can read nothing and says so.
- It keeps the assistant's parts (`get_time`, the template's instructions) and compacts long threads.

**On triggers** ([triggers.md](triggers.md)): a webhook for a new item, with a template such as `New issue
{{payload.issue.number}}: {{payload.issue.title}} {{payload.issue.html_url}}`, and a schedule for a sweep (`Sweep the
tracker`). The queue is per conversation (agent-wide state for apps is a later design, custom-agents.md), so **point
both at one fixed shared thread**: the queue then holds everything the agent has seen. Each run's approvals wait
without holding the run's time ([triggers.md](triggers.md#approvals-16)).

## Deep researcher

| Setting | Default | Meaning |
|---|---|---|
| `backgroundSubagents` | `true` | Long questions run in background subagents. Off, they wait like short ones. |
| `maxPlanSteps` | `8` | The most steps a plan may have (1-20). |

What it does (`runtime/src/kinds/researcher.ts`):

- **The plan** is an exposed conversation document, `research.plan`: the question and its steps (`s1`, `s2`, ...), each
  with a status (`pending`, `running`, `done`, `skipped`), the subagent answering it (`quick`, `deep`) and a note.
  `research_plan` writes it, `research_step` updates a step.
- **Sources** are an exposed document, `research.sources` (at most 200, 256 KiB): URL, title, what it supports (the
  claims, at most 20 per source) and the step. `research_source` adds one, or adds to what a known URL supports.
- **Subagents**, with the library's helper `defineSubagent`, run in parallel (the kind runs a round's tools in
  parallel, `toolExecution: "parallel"`): `research_quick` waits for its answer (foreground: short questions);
  `research_deep` runs in the background (long questions): the call returns at once, the child works in a background
  task, and its answer comes back to the thread as a message (`subagent.report`) that starts the next reply. Children
  get the template's MCP servers and guards, not the plan's tools or subagents of their own, and are told to end with
  their sources.
- **It compacts as it goes**: pi-durable's compaction, on, with background compaction before the threshold.
- **The report** is its answer in the thread once every step is done or skipped and no background answer is due: a
  summary, the findings by step and the sources as a numbered list. Reports as files wait for #14.

### Budgets and ceilings

A research run is often long or started by a trigger, so where its work is counted matters:

- **A trigger's run** is its thread's reply ([triggers.md](triggers.md#runs)): its maximum run time stops the reply,
  and its cost is the agent's usage between the run's start and end. Foreground subagents are part of the reply, so
  both cover them. Background work is not part of the reply: it would run past the run's end, uncounted by its budget
  and unstopped by its run time. So **in a run a trigger started, `research_deep` runs in the foreground**: the tool
  asks the library who started the run (`runAuthor`) and waits for the answer when it was a trigger. The trigger's
  budget and run time then cover all of the research, and the run's callback carries the report's excerpt.
- **Background work** (a person's runs) keeps the agent awake up to the hibernation ceiling, `AGENTS_BACKGROUND_MAX`
  (default 2 h, [hibernation.md](hibernation.md)): past it, the platform aborts the background subagent (its answer
  comes back as "ended") and the agent may hibernate. Stopping the thread doesn't stop background subagents (they are
  background on purpose); deleting the agent does.
- **Approvals** hold nothing: a subagent's tool call waiting for a person is a wait for a person, as anywhere.
- Team budgets (#18) will apply to the agent's usage as a whole, background work included.

## What the library gained

Both kinds use only `@pdp/runtime`'s public exports (`runtime/test/kinds.test.ts` reads every module of
`runtime/src/kinds/` and fails on any other import). What they needed became public API, usable by any custom agent:

- `defineSubagent({ background })`: a background subagent (above). `true`, or a function deciding per call (its first
  answer is kept, so a rerun after a restart goes the same way). Restart-safe like the foreground one: the tool finds
  its background task by its call, the task finds its child by ownership, and request IDs keep the task from being
  given twice or its answer posted twice. `SUBAGENT_REPORT` is the answer's entry kind.
- `runAuthor(api, context)`: who sent the message that started the run a tool call belongs to (a person, a service
  account or a trigger, as the platform recorded it).
- `runAgentProcess(choice)`: a definition, or a function of the agent's variables that returns one (`AgentChoice`).
- `@pdp/runtime/kinds`: the kinds, their documents, tools, prompts and settings parsers, to build a variant from.
- The test kit's `expectReplayed` checks background subagents too: given their task once, answer posted back once.

## The console

- **The template editor** (new template, new version) offers the kind and its settings: for triage the three tool lists
  and the approval switch, for the researcher the background switch and the plan's size. A template's versions list
  shows each version's kind.
- **The conversation** (the agent's Test tab and a conversation read from the Conversations tab) shows the thread's
  exposed documents next to the chat, from the documents endpoint, refreshed when the view's revision changes (the
  stream's `document.updated`): the triage queue as a table, the plan as a checklist, the sources as a list, and any
  other exposed document as JSON.

## Building a variant

A variant is a custom agent ([custom-agents.md](custom-agents.md), [the starter project](../examples/custom-agent/)):
import the parts from `@pdp/runtime/kinds` and change what differs, or copy the kind's module (it imports nothing
private). For example, triage that also files a weekly summary document, or a researcher with a cheaper model for its
quick subagents:

```ts
import { assistantExtension, defineAgent, defineExtension, defineSubagent, runAgentProcess, section } from "@pdp/runtime";
import { ResearchPlanDoc, ResearchSourcesDoc, parseResearcherSettings, researchPrompt, researchTools, SUBAGENT_INSTRUCTIONS } from "@pdp/runtime/kinds";

const settings = parseResearcherSettings(process.env.AGENT_KIND_SETTINGS);
await runAgentProcess(
	defineAgent({
		name: "cheap-researcher",
		documents: [ResearchPlanDoc, ResearchSourcesDoc],
		expose: [ResearchPlanDoc, ResearchSourcesDoc],
		extensions: ({ instructions }) => [
			assistantExtension(instructions),
			defineExtension({ name: "research", tools: researchTools(settings), sections: [section("research", () => researchPrompt(settings))] }),
			defineSubagent({ name: "research_quick", description: "A short question", instructions: SUBAGENT_INSTRUCTIONS, model: { provider: "openai", modelId: "small" } }),
		],
		settings: { toolExecution: "parallel", compaction: { enabled: true } },
	}),
);
```

Keep the document kinds (`triage.queue`, `research.plan`, `research.sources`) for the same shapes, so the console
renders them; a different shape needs a kind of its own.

## Tests

- `runtime/test/triage.test.ts`, on a fake MCP tracker (a Streamable HTTP server): a triggered run reads the new item,
  records it in its queue, drafts a comment and waits for approval with nothing written; the process crashes while it
  waits; after the restart the approval lands and the comment is posted once; the queue survives a crash and a
  graceful stop. A denied comment is never posted and the model is told why. A tool the settings don't name is refused;
  with `approveWrites` off nothing is asked.
- `runtime/test/researcher.test.ts`: a run writes the plan, runs a foreground and a background subagent at the same
  time, crashes while both work, resumes both, records both sources, reports once the background answer is back, and
  `expectReplayed` finds nothing repeated or lost; a triggered run keeps `research_deep` in the foreground; with
  background subagents off it waits.
- `runtime/test/kinds.test.ts` and `runtime/test/contract.test.ts`: selection by kind (the process runs the named
  kind; an unknown kind or bad settings exit 2; an activation for an unknown kind is refused and the runtime stays in
  standby), settings parsing, and the kinds' imports.
- `internal/agents` (integration): templates and versions with a kind, the refusals, keeping and changing a kind across
  versions, and the runtime's variables (`AGENT_KIND`, `AGENT_KIND_SETTINGS`, the write tools in `TOOL_APPROVALS`).
- The console: the kind fields and the documents panel's renderings.

## Not covered

- **An agent-wide queue.** The queue is per conversation: triggers should use one fixed thread. A view of an agent's
  state across its conversations needs its own access rule (custom-agents.md).
- **Stopping background subagents from the console.** They end on their own, at the ceiling, or with the agent.
- **A run's precise cost** for a person's research with background subagents: usage is per agent (triggers.md).
- **Reports as files** (#14) and per-tracker conveniences (a GitHub preset for the tool names).
