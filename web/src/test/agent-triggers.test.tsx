/*
 * An agent's Runs and Triggers tabs: the runs its triggers started (status, why, cost, callback, a link to the
 * conversation), the triggers (owners and admins add, pause and delete them; others only read), the secrets shown
 * once, and a trigger as a message's author in the chat.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { authorName } from "../pages/team/agents/message-meta";
import { runDuration } from "../pages/team/agents/runs-tab";
import { agent, agentId, team } from "./fixtures";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

type Run = Schemas["TriggerRun"];
type Trigger = Schemas["Trigger"];

class QuietEventSource {
  onerror: (() => void) | null = null;
  constructor(readonly url: string) {}
  addEventListener() {}
  close() {}
}

beforeEach(() => vi.stubGlobal("EventSource", QuietEventSource));

const triggerId = "31313131-3131-4313-8313-313131313131";

const run = (extra: Partial<Run>): Run => ({
  id: crypto.randomUUID(),
  agentId,
  triggerId,
  triggerName: "Daily digest",
  triggerKind: "schedule",
  firedBy: "schedule",
  fireId: "sc_x_1",
  threadId: null,
  status: "succeeded",
  reason: null,
  createdAt: "2026-10-20T08:00:00Z",
  costUsd: 0,
  requests: 0,
  ...extra,
});

const trigger = (extra: Partial<Trigger> = {}): Trigger => ({
  id: triggerId,
  agentId,
  kind: "schedule",
  name: "Daily digest",
  source: "person",
  paused: false,
  threadId: null,
  messageTemplate: "Summarise yesterday",
  schedule: { cron: "0 8 * * 1-5", timezone: "Europe/Paris", missedPolicy: "skip", nextFireAt: "2099-01-01T07:00:00Z" },
  consecutiveFailures: 0,
  createdAt: "2026-10-20T08:00:00Z",
  updatedAt: "2026-10-20T08:00:00Z",
  revision: 1,
  ...extra,
});

const settings: Schemas["TriggerSettings"] = {
  paused: false,
  effectiveConcurrentRuns: 1,
  effectiveRunSeconds: 1800,
  ceilingConcurrentRuns: 5,
  ceilingRunSeconds: 1800,
  revision: 1,
};

function platform(role: Schemas["TeamRole"], routes: Record<string, Handler> = {}) {
  return mockApi({
    ...shellRoutes("none", role),
    [`GET /v1/agents/${agentId}`]: () => agent(),
    [`GET /v1/agents/${agentId}/threads`]: () => [],
    [`GET /v1/agents/${agentId}/triggers`]: () => [trigger()],
    [`GET /v1/agents/${agentId}/trigger-settings`]: () => settings,
    ...routes,
  });
}

describe("an agent's runs", () => {
  it("lists each run with its trigger, status and why, cost and callback, and opens a run's conversation", async () => {
    const calls = platform("member", {
      [`GET /v1/agents/${agentId}/runs`]: () => [
        run({ status: "running", threadId: "t_live", startedAt: new Date(Date.now() - 65_000).toISOString(), costUsd: 0.012 }),
        run({ firedBy: "webhook", triggerName: "New issue", status: "skipped", reason: "budget" }),
        run({
          status: "succeeded",
          threadId: "t_done",
          startedAt: "2026-10-20T08:00:00Z",
          finishedAt: "2026-10-20T08:00:42Z",
          costUsd: 1.5,
          callbackStatus: "delivered",
        }),
        run({ firedBy: "manual", status: "stopped", reason: "max_run_time", threadId: "t_stop", startedAt: "2026-10-20T07:00:00Z" }),
      ],
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=runs`);
    const table = await screen.findByRole("table", { name: /Runs of/ });
    await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(5));
    expect(within(table).getByText("Running")).toBeInTheDocument();
    expect(within(table).getByText("over the daily budget")).toBeInTheDocument();
    expect(within(table).getByText("ran past its maximum time")).toBeInTheDocument();
    expect(within(table).getByText("$1.50")).toBeInTheDocument();
    expect(within(table).getByText("42 s")).toBeInTheDocument();
    expect(within(table).getByText("Delivered")).toBeInTheDocument();
    expect(within(table).getByText("Run now")).toBeInTheDocument();
    // A run that started links to its conversation in Test; a skipped one has none.
    const links = within(table).getAllByRole("link", { name: /Open the conversation/ });
    expect(links).toHaveLength(3);
    expect(links.map((l) => l.getAttribute("href"))).toContain(`/teams/${team.slug}/agents/${agentId}?tab=test&thread=t_done`);
    // The status filter asks the platform.
    await userEvent.setup().click(screen.getByRole("combobox", { name: "Status" }));
    await userEvent.setup().click(await screen.findByRole("option", { name: "Failed" }));
    await waitFor(() => expect(calls.some((c) => c.url === `/v1/agents/${agentId}/runs` && c.search.get("status") === "failed")).toBe(true));
  });

  it("counts a run's duration", () => {
    expect(runDuration({ startedAt: null })).toBe("—");
    expect(runDuration({ startedAt: "2026-10-20T08:00:00Z", finishedAt: "2026-10-20T08:01:05Z" })).toBe("1 min 05 s");
    expect(runDuration({ startedAt: "2026-10-20T08:00:00Z", finishedAt: "2026-10-20T10:03:00Z" })).toBe("2 h 03 min");
  });
});

describe("an agent's triggers", () => {
  it("lets an owner add a webhook and shows its secret once", async () => {
    const u = userEvent.setup();
    const created = trigger({ kind: "webhook", name: "New issue", schedule: null, webhookUrl: "https://pdp.example.com/v1/hooks/h_x" });
    const calls = platform("owner", {
      [`POST /v1/agents/${agentId}/triggers`]: () => ({ trigger: created, webhookSecret: "pdpwh_shown-once" }),
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=triggers`);
    expect(await screen.findByText("Daily digest")).toBeInTheDocument();
    expect(screen.getByText("0 8 * * 1-5 (Europe/Paris)")).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: /Add trigger/ }));
    await u.click(screen.getByRole("combobox", { name: "Kind" }));
    await u.click(await screen.findByRole("option", { name: /Webhook/ }));
    await u.type(screen.getByRole("textbox", { name: "Name" }), "New issue");
    await u.type(screen.getByRole("textbox", { name: /Only when the field/ }), "action");
    await u.type(screen.getByRole("textbox", { name: /has the value/ }), "opened");
    await u.type(screen.getByRole("textbox", { name: "Message" }), "Triage {{{{payload.title}}");
    await u.click(screen.getByRole("button", { name: "Add trigger" }));
    expect(await screen.findByText("Copy the secrets now.")).toBeInTheDocument();
    expect(screen.getByText("pdpwh_shown-once")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST" && c.url === `/v1/agents/${agentId}/triggers`);
    expect(post?.body).toEqual({ kind: "webhook", name: "New issue", messageTemplate: "Triage {{payload.title}}", filter: { path: "action", equals: "opened" } });
  });

  it("is read-only for members: no adding, pausing or deleting", async () => {
    platform("member");
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=triggers`);
    expect(await screen.findByText("Daily digest")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Add trigger/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Pause Daily digest/ })).toBeNull();
    expect(screen.getByRole("switch", { name: /Pause all/ })).toHaveAttribute("aria-disabled", "true");
  });

  it("pauses a trigger with its revision", async () => {
    const u = userEvent.setup();
    const calls = platform("admin", {
      [`PATCH /v1/agents/${agentId}/triggers/${triggerId}`]: () => ({ trigger: trigger({ paused: true, pausedReason: "user", revision: 2 }) }),
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=triggers`);
    await u.click(await screen.findByRole("button", { name: "Pause Daily digest" }));
    await waitFor(() => {
      const patch = calls.find((c) => c.method === "PATCH");
      expect(patch?.body).toEqual({ paused: true });
      expect(patch?.headers.get("If-Match")).toBe('"1"');
    });
  });
});

describe("a trigger's message in the chat", () => {
  it("is named as the trigger, never as a person", () => {
    expect(authorName({ kind: "trigger", id: triggerId, name: "Daily digest" }, "someone", false)).toBe("Trigger: Daily digest");
  });
});
