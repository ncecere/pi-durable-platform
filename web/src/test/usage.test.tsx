/* Usage reports: Team › Usage (totals, rows, Group by, one agent), Admin › Usage, and an agent's Overview card. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { dayLabel } from "../components/usage/labels";
import { agent, agentId, team } from "./fixtures";
import { type Call, type Handler, mockApi, renderApp, shellRoutes } from "./harness";

type Row = Schemas["UsageRow"];
type Report = Schemas["UsageReport"];

const row = (extra: Partial<Row> = {}): Row => ({
  managed: false,
  requests: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
  costUsd: 0,
  unpricedRequests: 0,
  ...extra,
});

const total = row({ requests: 1234, inputTokens: 2_500_000, outputTokens: 48_000, costUsd: 12.4, unpricedRequests: 3 });

const rowsBy: Record<Report["groupBy"], Row[]> = {
  day: [row({ day: "2026-10-15", requests: 1000, inputTokens: 2_000_000, outputTokens: 40_000, costUsd: 10 }), row({ day: "2026-10-16", requests: 234, inputTokens: 500_000, outputTokens: 8_000, costUsd: 2.4, unpricedRequests: 3 })],
  model: [
    row({ modelId: "m1", modelName: "Fake One", providerName: "LiteLLM gateway", requests: 1000, inputTokens: 2_400_000, outputTokens: 40_000, costUsd: 12.4 }),
    row({ requests: 234, inputTokens: 100_000, outputTokens: 8_000, costUsd: 0, unpricedRequests: 3 }),
  ],
  agent: [
    row({ agentId, agentName: "Helper", requests: 1200, inputTokens: 2_400_000, outputTokens: 47_000, costUsd: 12.4 }),
    row({ managed: true, requests: 34, inputTokens: 100_000, outputTokens: 1_000, costUsd: 0.001, unpricedRequests: 3 }),
  ],
  "agent,day": [
    row({ agentId, agentName: "Helper", day: "2026-10-15", requests: 1000, inputTokens: 2_000_000, outputTokens: 40_000, costUsd: 10 }),
    row({ agentId, agentName: "Helper", day: "2026-10-16", requests: 200, inputTokens: 400_000, outputTokens: 7_000, costUsd: 2.4 }),
    row({ managed: true, day: "2026-10-16", requests: 34, inputTokens: 100_000, outputTokens: 1_000, costUsd: 0.001, unpricedRequests: 3 }),
  ],
  team: [row({ teamId: team.id, teamSlug: team.slug, teamName: team.name, requests: 1234, inputTokens: 2_500_000, outputTokens: 48_000, costUsd: 12.4, unpricedRequests: 3 })],
};

/** Answers with the rows of the requested grouping. */
const report: Handler = (_body, call): Report => {
  const groupBy = (call.search.get("groupBy") ?? "day") as Report["groupBy"];
  return { from: call.search.get("from") ?? "2026-09-18", to: call.search.get("to") ?? "2026-10-17", groupBy, rows: rowsBy[groupBy], total };
};

const usagePath = `/v1/teams/${team.slug}/usage`;
const usageCalls = (calls: Call[], url = usagePath) => calls.filter((c) => c.method === "GET" && c.url === url);
const teamRoutes = (extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes("none", "member"),
  [`GET ${usagePath}`]: report,
  [`GET /v1/agents/${agentId}`]: () => agent(),
  ...extra,
});

describe("Team › Usage", () => {
  it("shows the totals and a row per model, the platform default model included, and the unpriced note", async () => {
    const calls = mockApi(teamRoutes());
    renderApp(`/teams/${team.slug}/usage?groupBy=model`);
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Usage by model" });
    expect(await within(table).findByText("Fake One")).toBeInTheDocument();
    expect(within(table).getByText("LiteLLM gateway")).toBeInTheDocument();
    expect(within(table).getByText("Platform default model")).toBeInTheDocument();
    expect(within(table).getByText("$0.00")).toBeInTheDocument();
    // The totals: requests, compact tokens and the estimated cost.
    expect(screen.getByText("1,234")).toBeInTheDocument();
    expect(screen.getByText("2.5M")).toBeInTheDocument();
    expect(screen.getByText("48K")).toBeInTheDocument();
    expect(screen.getAllByText("$12.40").length).toBeGreaterThan(0);
    expect(screen.getByText("3 requests used models without prices and aren't in the cost.")).toBeInTheDocument();
    const last = usageCalls(calls).at(-1)!;
    expect(last.search.get("groupBy")).toBe("model");
    expect(last.search.get("from")).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(last.search.get("to")).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(last.search.has("agentId")).toBe(false);
  });

  it("asks for the new grouping when Group by changes, and keeps it in the address", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes());
    const { router } = renderApp(`/teams/${team.slug}/usage`);
    expect(await screen.findByRole("table", { name: "Usage by day" })).toBeInTheDocument();
    expect(usageCalls(calls).at(-1)!.search.get("groupBy")).toBe("day");
    // By day, tokens per day as bars too.
    expect(await screen.findByRole("img", { name: /^Tokens per day: 2.5M in total/ })).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Agent" }));
    await waitFor(() => expect(usageCalls(calls).at(-1)!.search.get("groupBy")).toBe("agent"));
    expect(router.state.location.search).toMatchObject({ groupBy: "agent" });
    const table = await screen.findByRole("table", { name: "Usage by agent" });
    expect(within(table).getByRole("link", { name: "Helper" })).toHaveAttribute("href", `/teams/${team.slug}/agents/${agentId}`);
    expect(within(table).getByText("Service-managed agents")).toBeInTheDocument();
    expect(within(table).getByText("<$0.01")).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /^Tokens per day/ })).toBeNull();
  });

  it("shows a row per agent and day when grouped by agent and day", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes());
    const { router } = renderApp(`/teams/${team.slug}/usage`);
    await screen.findByRole("table", { name: "Usage by day" });
    await u.click(screen.getByRole("button", { name: "Agent by day" }));
    await waitFor(() => expect(usageCalls(calls).at(-1)!.search.get("groupBy")).toBe("agent,day"));
    expect(router.state.location.search).toMatchObject({ groupBy: "agent,day" });
    const table = await screen.findByRole("table", { name: "Usage by agent and day" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByRole("link", { name: "Helper" })).toBeInTheDocument();
    expect(within(rows[0]!).getByText(dayLabel("2026-10-15"))).toBeInTheDocument();
    expect(within(rows[1]!).getByText(dayLabel("2026-10-16"))).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Service-managed agents")).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: /^Tokens per day/ })).toBeNull();
  });

  it("narrows the report to the agent in ?agentId=, with a way to show all agents", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes());
    const { router } = renderApp(`/teams/${team.slug}/usage?agentId=${agentId}`);
    expect(await screen.findByText("Only Helper")).toBeInTheDocument();
    await waitFor(() => expect(usageCalls(calls).at(-1)!.search.get("agentId")).toBe(agentId));
    await u.click(screen.getByRole("button", { name: "Show all agents" }));
    await waitFor(() => expect(usageCalls(calls).at(-1)!.search.has("agentId")).toBe(false));
    expect(router.state.location.search).not.toHaveProperty("agentId");
    expect(screen.queryByText("Only Helper")).toBeNull();
  });
});

describe("Admin › Usage", () => {
  it("groups every team's usage by team, with links to the teams", async () => {
    const u = userEvent.setup();
    const calls = mockApi({ ...shellRoutes("platform_auditor", null), "GET /v1/admin/usage": report });
    renderApp("/admin/usage");
    expect(await screen.findByRole("heading", { level: 1, name: "Usage" })).toBeInTheDocument();
    await screen.findByRole("table", { name: "Usage by day" });
    await u.click(screen.getByRole("button", { name: "Team" }));
    await waitFor(() => expect(usageCalls(calls, "/v1/admin/usage").at(-1)!.search.get("groupBy")).toBe("team"));
    const table = await screen.findByRole("table", { name: "Usage by team" });
    expect(within(table).getByRole("link", { name: team.name })).toHaveAttribute("href", `/admin/teams/${team.slug}`);
  });

  it("narrows the report to one connected app's usage, kept in the address", async () => {
    const u = userEvent.setup();
    const app = "https://chat.example.com/oauth/client-metadata.json";
    const calls = mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/usage": report,
      "GET /v1/admin/oauth-clients": () => [{ clientId: app, name: "Team Chat", kind: "metadata", people: 12, lastActiveAt: "2026-10-17T10:00:00Z", trusted: true }],
    });
    const { router } = renderApp("/admin/usage");
    await screen.findByRole("table", { name: "Usage by day" });
    expect(usageCalls(calls, "/v1/admin/usage").at(-1)!.search.has("clientId")).toBe(false);
    const select = screen.getByRole("combobox", { name: "App" });
    await screen.findByRole("option", { name: "Team Chat (12 people)" });
    await u.selectOptions(select, app);
    await waitFor(() => expect(usageCalls(calls, "/v1/admin/usage").at(-1)!.search.get("clientId")).toBe(app));
    expect(router.state.location.search).toMatchObject({ app });
  });
});

describe("an agent's Overview", () => {
  it("shows the last 30 days' usage, with a link to the team's Usage for the agent", async () => {
    const calls = mockApi(teamRoutes());
    renderApp(`/teams/${team.slug}/agents/${agentId}`);
    const card = (await screen.findByRole("heading", { name: "Usage (last 30 days)" })).closest("section")!;
    expect(await within(card as HTMLElement).findByText("1,234")).toBeInTheDocument();
    expect(within(card as HTMLElement).getByText("2.5M input · 48K output")).toBeInTheDocument();
    expect(within(card as HTMLElement).getByText("$12.40")).toBeInTheDocument();
    expect(within(card as HTMLElement).getByRole("link", { name: "View usage of Helper" })).toHaveAttribute("href", `/teams/${team.slug}/usage?agentId=${agentId}`);
    const call = usageCalls(calls).at(-1)!;
    expect(call.search.get("agentId")).toBe(agentId);
    expect(call.search.get("groupBy")).toBe("day");
  });
});
