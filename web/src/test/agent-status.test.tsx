/*
 * The agent page's status badge follows the agent without a reload: a message accepted by the Test chat wakes it
 * (Awake), Hibernate puts it back (Hibernating), and the agent's thread events (GET /v1/agents/events) say when
 * a reply starts (an app's message, another tab) or the agent changed.
 */
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, team } from "./fixtures";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

type ThreadEvent = Schemas["AgentThreadEvent"];

/** EventSource stand-in whose streams the test can push events into. */
class FakeEventSource {
  static all: FakeEventSource[] = [];
  onerror: (() => void) | null = null;
  private listeners = new Map<string, ((e: { data: string }) => void)[]>();
  constructor(readonly url: string) {
    FakeEventSource.all.push(this);
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  close() {
    FakeEventSource.all = FakeEventSource.all.filter((s) => s !== this);
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) fn({ data: JSON.stringify(data) });
  }
}

/** The agents' stream (not a thread's): what the page opens for its own agent. */
const agentStream = () => FakeEventSource.all.find((s) => s.url.startsWith("/v1/agents/events"));

const threadEvent = (busy: boolean): ThreadEvent => ({
  agentId,
  threadId: "t1",
  deleted: false,
  thread: {
    threadId: "t1",
    title: "Hello",
    createdAt: 0,
    updatedAt: 0,
    busy,
    lastReplyAt: null,
    lastError: null,
    visibility: "shared",
    participants: [],
  },
});

/** A platform whose agent is awake or hibernating: Hibernate and a message change it, as the real one would. */
function platform(running: boolean) {
  const state = { running };
  const routes: Record<string, Handler> = {
    ...shellRoutes("none", "owner"),
    [`GET /v1/agents/${agentId}`]: () => agent({ running: state.running }),
    [`GET /v1/teams/${team.slug}/agents`]: () => [agent({ running: state.running })],
    [`GET /v1/agents/${agentId}/threads`]: () => [],
    [`POST /v1/agents/${agentId}/hibernate`]: () => {
      state.running = false;
      return null;
    },
  };
  const calls = mockApi(routes);
  // A message to any thread is accepted and wakes the agent.
  const inner = globalThis.fetch;
  vi.stubGlobal("fetch", async (input: Request | string, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), "http://localhost"), init);
    if (req.method === "POST" && /^\/v1\/agents\/[^/]+\/threads\/[^/]+\/messages$/.test(new URL(req.url).pathname)) {
      state.running = true;
      return new Response(JSON.stringify({ data: null }), { status: 202, headers: { "Content-Type": "application/json" } });
    }
    return inner(req);
  });
  return { state, calls };
}

/** Every status the page shows (the badge, and the Overview's) says `label`. */
const statuses = () => screen.getAllByText(/^(Awake|Hibernating)$/).map((e) => e.textContent);
const showsStatus = (label: "Awake" | "Hibernating") =>
  waitFor(() => {
    const all = statuses();
    expect(all.length).toBeGreaterThan(0);
    expect(all.every((t) => t === label)).toBe(true);
  });

beforeEach(() => {
  FakeEventSource.all = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});

describe("agent status badge", () => {
  it("goes from Hibernating to Awake when a test message is accepted, and back after Hibernate, without a reload", async () => {
    const u = userEvent.setup();
    platform(false);
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    await showsStatus("Hibernating");

    await u.type(await screen.findByRole("textbox", { name: "Message" }), "Hello");
    await u.click(screen.getByRole("button", { name: "Send message" }));
    await showsStatus("Awake");

    await u.click(await screen.findByRole("button", { name: /Hibernate/ }));
    await showsStatus("Hibernating");

    await u.type(screen.getByRole("textbox", { name: "Message" }), "Again");
    await u.click(screen.getByRole("button", { name: "Send message" }));
    await showsStatus("Awake");
  });

  it("follows the agent's thread events: awake when a reply starts (an app's message), current again when a thread finishes", async () => {
    const { state, calls } = platform(false);
    renderApp(`/teams/${team.slug}/agents/${agentId}`);
    await showsStatus("Hibernating");
    await waitFor(() => expect(agentStream()).toBeDefined());
    expect(new URL(agentStream()!.url, "http://localhost").searchParams.get("agentId")).toBe(agentId);

    // An app's message wakes the agent: a reply starts.
    state.running = true;
    act(() => agentStream()!.emit("thread", threadEvent(true)));
    await showsStatus("Awake");

    // The agent hibernated meanwhile (idle); the next event, a reply finishing, brings the badge up to date.
    state.running = false;
    act(() => agentStream()!.emit("thread", threadEvent(false)));
    await showsStatus("Hibernating");

    // A reconnect (the stream may have missed events) refreshes it too.
    state.running = true;
    const before = calls.filter((c) => c.method === "GET" && c.url === `/v1/agents/${agentId}`).length;
    act(() => agentStream()!.emit("ready", {}));
    await showsStatus("Awake");
    expect(calls.filter((c) => c.method === "GET" && c.url === `/v1/agents/${agentId}`).length).toBeGreaterThan(before);
  });

  it("says on the Overview why the agent's runtime was refused, and which version wrote its data", async () => {
    mockApi({
      ...shellRoutes("none", "owner"),
      [`GET /v1/agents/${agentId}`]: () =>
        agent({
          storageVersion: "1.4.0",
          runtimeRefused: {
            runtimeVersion: "1.3.0",
            storageVersion: "1.4.0",
            at: new Date().toISOString(),
            message: "This agent's data was written by a runtime with pi-durable 1.4.0; this runtime has pi-durable 1.3.0.",
          },
        }),
      [`GET /v1/teams/${team.slug}/agents`]: () => [agent()],
      [`GET /v1/agents/${agentId}/threads`]: () => [],
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}`);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The agent can't start on its runtime");
    expect(alert).toHaveTextContent("this runtime has pi-durable 1.3.0");
    expect(screen.getByText(/Written by pi-durable 1\.4\.0/)).toBeInTheDocument();
  });
});
