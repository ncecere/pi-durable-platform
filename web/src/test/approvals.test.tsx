/*
 * Tool approvals in the console: the agent page's Approvals tab lists calls waiting for a person, with Approve and
 * Deny for whoever may decide (the platform says who: canDecide), and recent decisions; the test chat shows a
 * pending call's approval on the call, with the same choices.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, team } from "./fixtures";
import { mockApi, renderApp, shellRoutes } from "./harness";

type ToolApproval = Schemas["ToolApproval"];

const approval = (over: Partial<ToolApproval> = {}): ToolApproval => ({
  id: "ap_one",
  agentId,
  agentName: "Helper",
  threadId: "t1",
  threadTitle: "Clean up the CRM",
  tool: "mcp__crm__delete_record",
  callId: "c1",
  arguments: { record: "42", headers: "[redacted]" },
  requestedFor: { kind: "user", id: "u1", name: "Ann" },
  status: "pending",
  requestedAt: "2026-10-20T10:00:00Z",
  expiresAt: "2026-10-21T10:00:00Z",
  canDecide: true,
  ...over,
});

type View = Schemas["ChatView"];

/** EventSource stand-in: a thread's delta stream gets a snapshot of `view`; other streams stay quiet. */
let view: View | undefined;
class FakeEventSource {
  onerror: (() => void) | null = null;
  private listeners: { type: string; fn: (e: { data: string }) => void }[] = [];
  constructor(readonly url: string) {
    setTimeout(() => {
      if (!view || !url.includes("format=delta")) return;
      for (const { type, fn } of this.listeners) if (type === "snapshot") fn({ data: JSON.stringify({ type: "snapshot", seq: 1, v: 1, view }) });
    }, 0);
  }
  addEventListener(type: string, fn: (e: { data: string }) => void) {
    this.listeners.push({ type, fn });
  }
  close() {}
}

beforeEach(() => {
  view = undefined;
  vi.stubGlobal("EventSource", FakeEventSource);
});

describe("the Approvals tab", () => {
  it("approves a waiting call for someone who may decide, and says when it isn't theirs", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...shellRoutes("none", "owner"),
      [`GET /v1/agents/${agentId}`]: () => agent(),
      [`GET /v1/teams/${team.slug}/agents`]: () => [agent()],
      [`GET /v1/agents/${agentId}/threads`]: () => [],
      [`GET /v1/agents/${agentId}/approvals`]: (_b, call) =>
        call.search.get("status") === "decided"
          ? [approval({ id: "ap_old", tool: "get_time", status: "denied", reason: "Not now", decidedBy: { kind: "user", name: "Bo" }, decidedAt: "2026-10-20T09:00:00Z", canDecide: false })]
          : [approval(), approval({ id: "ap_two", tool: "mcp__crm__update", canDecide: false })],
      [`POST /v1/agents/${agentId}/approvals/ap_one`]: () => approval({ status: "approved", canDecide: false }),
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=approvals`);
    const waiting = await screen.findByRole("table", { name: /waiting for approval/ });
    expect(await within(waiting).findByText("mcp__crm__delete_record")).toBeInTheDocument();
    expect(within(waiting).getAllByText("Ann")).toHaveLength(2);
    expect(within(waiting).getByText("Not yours to decide")).toBeInTheDocument();
    const decided = await screen.findByRole("table", { name: /Decided tool calls/ });
    expect(await within(decided).findByText("Denied by Bo: Not now")).toBeInTheDocument();

    await u.click(within(waiting).getByRole("button", { name: "Approve mcp__crm__delete_record" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === `/v1/agents/${agentId}/approvals/ap_one`)).toBe(true));
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({ decision: "approve" });
  });
});

describe("the test chat", () => {
  it("shows a call waiting for approval on the call, with Approve and Deny, and sends the reason", async () => {
    const u = userEvent.setup();
    const pending = { id: "ap_one", callId: "c1", tool: "get_time", status: "pending" as const, requestedAt: 1, expiresAt: Date.now() + 3_600_000, requestedFor: { kind: "user" as const, id: "u1", name: "Ann" } };
    view = {
      busy: true,
      tools: [],
      messages: [
        { id: "e1", role: "user", text: "What time is it?" },
        { id: "e2", role: "assistant", text: "", toolCalls: [{ id: "c1", name: "get_time", arguments: {}, approval: pending }] },
      ],
      approvals: [{ ...pending, arguments: {} }],
    };
    const now = Date.now();
    const calls = mockApi({
      ...shellRoutes("none", "owner"),
      [`GET /v1/agents/${agentId}`]: () => agent({ running: true }),
      [`GET /v1/teams/${team.slug}/agents`]: () => [agent({ running: true })],
      [`GET /v1/agents/${agentId}/threads`]: () => [
        { threadId: "t1", title: "Time", createdAt: now, updatedAt: now, busy: true, lastReplyAt: null, lastError: null, visibility: "shared", participants: [] },
      ],
      [`GET /v1/agents/${agentId}/approvals`]: () => [approval({ tool: "get_time" })],
      [`POST /v1/agents/${agentId}/approvals/ap_one`]: () => approval({ status: "denied", canDecide: false }),
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=test`);
    expect(await screen.findByText("get_time waits for approval")).toBeInTheDocument();
    expect(screen.getByText(/Asked for Ann/)).toBeInTheDocument();
    await u.type(screen.getByRole("textbox", { name: /Reason/ }), "Not now");
    await u.click(screen.getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === `/v1/agents/${agentId}/approvals/ap_one`)).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({ decision: "deny", reason: "Not now" });
  });
});
