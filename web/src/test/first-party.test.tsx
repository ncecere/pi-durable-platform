/*
 * First-party agents in the console (docs/first-party-agents.md): the template editor offers the kind and its
 * settings, the versions list names a version's kind, and the test chat shows the conversation's exposed documents
 * (the triage queue as a table, the research plan as a checklist, the sources as a list), reloading one when the
 * stream says it changed (document.updated).
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, team } from "./fixtures";
import { type Call, type Handler, mockApi, renderApp, Reply, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);

type View = Schemas["ChatView"];

/** EventSource stand-in: the thread's delta stream sends a snapshot of `view`, then `later` events when told. */
let view: View | undefined;
let streams: FakeEventSource[] = [];
class FakeEventSource {
  onerror: (() => void) | null = null;
  private listeners: { type: string; fn: (e: { data: string }) => void }[] = [];
  constructor(readonly url: string) {
    streams.push(this);
    setTimeout(() => {
      if (!view || !url.includes("format=delta")) return;
      this.emit({ type: "snapshot", seq: 1, v: 1, view });
    }, 0);
  }
  emit(event: { type: string } & Record<string, unknown>) {
    for (const { type, fn } of this.listeners) if (type === event.type) fn({ data: JSON.stringify(event) });
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    this.listeners.push({ type, fn });
  }
  close() {}
}

beforeEach(() => {
  view = undefined;
  streams = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});

const now = Date.now();
const chatRoutes = (extra: Record<string, Handler>): Record<string, Handler> => ({
  ...shellRoutes("none", "owner"),
  [`GET /v1/agents/${agentId}`]: () => agent({ running: true }),
  [`GET /v1/teams/${team.slug}/agents`]: () => [agent({ running: true })],
  [`GET /v1/agents/${agentId}/threads`]: () => [
    { threadId: "t1", title: "Triage", createdAt: now, updatedAt: now, busy: false, lastReplyAt: null, lastError: null, visibility: "shared", participants: [] },
  ],
  ...extra,
});

const doc = (kind: string, revision: string, value: unknown) => ({ kind, version: 1, revision, value });

describe("the conversation's documents", () => {
  it("shows the triage queue as a table and reloads it when the stream says it changed", async () => {
    view = {
      busy: false,
      tools: [],
      messages: [
        { id: "e1", role: "user", text: "New issue #7" },
        { id: "e2", role: "assistant", text: "Triaged." },
      ],
      documents: { "triage.queue": { revision: "r1" } },
    };
    let revision = "r1";
    const item = { id: "7", title: "Crash on start", url: "https://tracker.example/7", decision: "bug", labels: ["bug"], reply: "Which version?", summary: "", links: [], seenAt: 1, updatedAt: 1 };
    const calls = mockApi(
      chatRoutes({
        [`GET /v1/agents/${agentId}/threads/t1/documents/triage.queue`]: () =>
          doc("triage.queue", revision, { items: [{ ...item, status: revision === "r1" ? "drafted" : "done" }] }),
      }),
    );
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const table = await screen.findByRole("table", { name: "Triage queue" });
    expect(await within(table).findByText("7 Crash on start")).toBeInTheDocument();
    expect(within(table).getByRole("link", { name: "7 Crash on start" })).toHaveAttribute("href", "https://tracker.example/7");
    expect(within(table).getByText("drafted")).toBeInTheDocument();
    expect(within(table).getByText(/Labels: bug/)).toBeInTheDocument();

    revision = "r2";
    streams.find((e) => e.url.includes("format=delta"))!.emit({ type: "document.updated", seq: 2, kind: "triage.queue", revision: "r2" });
    expect(await within(table).findByText("done")).toBeInTheDocument();
    expect(made(calls, "GET", `/v1/agents/${agentId}/threads/t1/documents/triage.queue`)).toHaveLength(2);
  });

  it("shows the research plan as a checklist and the sources as a list; a link is only http(s)", async () => {
    view = {
      busy: false,
      tools: [],
      messages: [{ id: "e1", role: "user", text: "Research durability" }],
      documents: { "research.sources": { revision: "s1" }, "research.plan": { revision: "p1" }, "example.board": { revision: "b1" } },
    };
    mockApi(
      chatRoutes({
        [`GET /v1/agents/${agentId}/threads/t1/documents/research.plan`]: () =>
          doc("research.plan", "p1", {
            question: "How do agents survive restarts?",
            steps: [
              { id: "s1", text: "Commits", status: "done", subagent: "quick", note: "" },
              { id: "s2", text: "Tasks", status: "running", subagent: "deep", note: "" },
            ],
          }),
        [`GET /v1/agents/${agentId}/threads/t1/documents/research.sources`]: () =>
          doc("research.sources", "s1", {
            sources: [
              { url: "https://a.example/commits", title: "Commits", supports: ["commits are atomic"], step: "s1" },
              { url: "javascript:alert(1)", title: "Odd", supports: [], step: "" },
            ],
          }),
        [`GET /v1/agents/${agentId}/threads/t1/documents/example.board`]: () => doc("example.board", "b1", { tasks: [{ id: "c1", text: "Milk" }] }),
      }),
    );
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    const plan = await screen.findByRole("region", { name: "How do agents survive restarts?" });
    expect(within(plan).getByText("1 of 2 done")).toBeInTheDocument();
    expect(within(plan).getByText("Tasks")).toBeInTheDocument();
    expect(within(plan).getByText("running · deep subagent")).toBeInTheDocument();
    const sources = await screen.findByRole("list", { name: "Research sources" });
    expect(within(sources).getByRole("link", { name: /Commits/ })).toHaveAttribute("href", "https://a.example/commits");
    expect(within(sources).getByText("commits are atomic")).toBeInTheDocument();
    expect(within(sources).queryByRole("link", { name: /Odd/ })).toBeNull();
    // Another agent's document: as JSON.
    expect(await screen.findByText(/"Milk"/)).toBeInTheDocument();
  });
});

describe("the template editor", () => {
  const templateId = "71717171-7171-4171-8171-717171717171";
  const template: Schemas["AgentTemplate"] = {
    id: templateId,
    slug: "triage",
    name: "Triage",
    description: "",
    teamId: team.id,
    teamSlug: team.slug,
    teamName: team.name,
    status: "private",
    archived: false,
    currentVersion: 1,
    revision: 1,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-01T10:00:00Z",
  };
  const triageSettings = { listTools: ["mcp__gh__list_issues"], readTools: [], writeTools: ["mcp__gh__add_comment"], approveWrites: true };
  const versions: Schemas["AgentTemplateVersion"][] = [
    { version: 1, instructions: "Triage.", notes: "Initial version", kind: "triage", kindSettings: triageSettings, createdAt: "2026-10-01T10:00:00Z" },
  ];
  const routes = (extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes("none", "editor"),
    [`GET /v1/teams/${team.slug}/agent-templates`]: () => [template],
    [`GET /v1/teams/${team.slug}/models`]: () => [],
    [`GET /v1/teams/${team.slug}/mcp-servers`]: () => [],
    [`GET /v1/agent-templates/${templateId}`]: () => template,
    [`GET /v1/agent-templates/${templateId}/versions`]: () => versions,
    ...extra,
  });

  it("creates a triage template with the tools it lists, reads and writes through", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes({ [`POST /v1/teams/${team.slug}/agent-templates`]: () => new Reply(201, { data: template }) }));
    renderApp(`/teams/${team.slug}/templates`);
    await u.click(await screen.findByRole("button", { name: "New template" }));
    const dialog = await screen.findByRole("dialog", { name: "New team template" });
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Triage");
    await u.type(within(dialog).getByRole("textbox", { name: "Slug" }), "triage");
    await u.type(within(dialog).getByRole("textbox", { name: "Instructions" }), "Triage our issues.");
    await u.click(within(dialog).getByRole("radio", { name: /^Triage/ }));
    await u.type(within(dialog).getByRole("textbox", { name: "Tools that list new items" }), "mcp__gh__List_issues{Enter}");
    await u.type(within(dialog).getByRole("textbox", { name: "Tools that write" }), "mcp__gh__add_comment{Enter}");
    expect(within(dialog).getByRole("switch", { name: "Ask a person before every write" })).toBeChecked();
    await u.click(within(dialog).getByRole("button", { name: "Create template" }));
    await waitFor(() => expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)).toHaveLength(1));
    expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)[0]!.body).toMatchObject({
      kind: "triage",
      kindSettings: { listTools: ["mcp__gh__List_issues"], readTools: [], writeTools: ["mcp__gh__add_comment"], approveWrites: true },
    });
  });

  it("names a version's kind, keeps it in a new version, and sends the new kind's settings when it changes", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes({ [`POST /v1/agent-templates/${templateId}/versions`]: () => new Reply(201, { data: template }) }));
    renderApp(`/templates/${templateId}?tab=versions`);
    expect(await screen.findByText("Triage", { selector: "[data-tone]" })).toBeInTheDocument();
    const post = () => made(calls, "POST", `/v1/agent-templates/${templateId}/versions`);

    await u.click(screen.getByRole("button", { name: "New version" }));
    let dialog = await screen.findByRole("dialog", { name: "New version of Triage" });
    await waitFor(() => expect(within(dialog).getByRole("radio", { name: /^Triage/ })).toBeChecked());
    expect(within(dialog).getByText("mcp__gh__list_issues")).toBeInTheDocument();
    await u.click(within(dialog).getByRole("button", { name: "Save v2" }));
    await waitFor(() => expect(post()).toHaveLength(1));
    expect(post()[0]!.body).not.toHaveProperty("kind");
    expect(post()[0]!.body).not.toHaveProperty("kindSettings");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "New version of Triage" })).toBeNull());

    await u.click(screen.getByRole("button", { name: "New version" }));
    dialog = await screen.findByRole("dialog", { name: "New version of Triage" });
    await u.click(await within(dialog).findByRole("radio", { name: /^Deep researcher/ }));
    await u.click(within(dialog).getByRole("switch", { name: "Long questions run in the background" }));
    await u.click(within(dialog).getByRole("button", { name: "Save v2" }));
    await waitFor(() => expect(post()).toHaveLength(2));
    expect(post()[1]!.body).toMatchObject({ kind: "researcher", kindSettings: { backgroundSubagents: false, maxPlanSteps: 8 } });
  });
});
