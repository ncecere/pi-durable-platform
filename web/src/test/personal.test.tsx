/*
 * Personal workspaces: Admin › Settings (the defaults), a personal workspace's admin page (its limits), the
 * Admin › Teams kind filter, the team switcher, the workspace's own pages, and "personal" on models.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, meFor, member, model, modelProvider, myTeam, page, providerId, providerKinds, team, teamSummary } from "./fixtures";
import { type Call, type Handler, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);

const personalTeam: Schemas["Team"] = {
  ...team,
  id: "21212121-2121-4212-8212-212121212121",
  slug: "personal-222222222222",
  name: "Personal",
  description: "",
  kind: "personal",
};
const personalPath = `/v1/teams/${personalTeam.slug}`;
const myPersonal = myTeam("owner", { id: personalTeam.id, slug: personalTeam.slug, name: "Personal", kind: "personal" });

const settings = (extra: Partial<Schemas["PersonalWorkspaceSettings"]> = {}): Schemas["PersonalWorkspaceSettings"] => ({
  enabled: true,
  maxAgents: 5,
  maxRunning: 2,
  monthlyBudgetUsd: null,
  revision: 7,
  updatedAt: "2026-10-18T10:00:00Z",
  ...extra,
});

const limits = (extra: Partial<Schemas["WorkspaceLimits"]> = {}): Schemas["WorkspaceLimits"] => ({
  kind: "personal",
  maxAgents: 5,
  maxRunning: 2,
  monthlyBudgetUsd: null,
  overridden: [],
  agentCount: 2,
  runningCount: 1,
  ...extra,
});

/** A personal workspace's own routes, for its owner or platform staff. */
const personalRoutes = (role: Schemas["TeamRole"] | null): Record<string, Handler> => ({
  [`GET ${personalPath}`]: (): Schemas["TeamView"] => (role ? { team: personalTeam, role } : { team: personalTeam }),
  [`GET ${personalPath}/members`]: () => [member("owner")],
  [`GET ${personalPath}/limits`]: () => limits({ monthlyBudgetUsd: 12.5 }),
  [`GET ${personalPath}/audit`]: () => page([]),
});

describe("Admin › Settings", () => {
  it("loads the personal workspace defaults and saves them with If-Match and numbers", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/settings/personal-workspaces": () => settings(),
      "PUT /v1/admin/settings/personal-workspaces": (body) => settings({ ...(body as object), revision: 8 }),
    });
    renderApp("/admin/settings");
    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeInTheDocument();
    expect(await screen.findByRole("switch", { name: /Give everyone who signs in a personal workspace/ })).toBeChecked();
    expect(screen.getByText(/Recorded; not enforced yet/)).toBeInTheDocument();
    const maxAgents = screen.getByRole("textbox", { name: "Max agents" });
    await u.clear(maxAgents);
    await u.type(maxAgents, "10");
    await u.type(screen.getByRole("textbox", { name: /Monthly budget/ }), "25.5");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")).toHaveLength(1));
    const put = made(calls, "PUT", "/v1/admin/settings/personal-workspaces")[0]!;
    expect(put.headers.get("If-Match")).toBe('"7"');
    expect(put.body).toEqual({ enabled: true, ownProviders: false, ownMcpServers: false, maxAgents: 10, maxRunning: 2, monthlyBudgetUsd: 25.5 });
  });

  it("sends null for an emptied budget and refuses a number out of range", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/settings/personal-workspaces": () => settings({ monthlyBudgetUsd: 50 }),
      "PUT /v1/admin/settings/personal-workspaces": (body) => settings({ ...(body as object), revision: 8 }),
    });
    renderApp("/admin/settings");
    const running = await screen.findByRole("textbox", { name: "Max running at once" });
    await u.clear(running);
    await u.type(running, "5000");
    expect(await screen.findByText("Enter a whole number from 0 to 1,000.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save settings" })).toBeDisabled();
    await u.clear(running);
    await u.type(running, "3");
    await u.clear(screen.getByRole("textbox", { name: /Monthly budget/ }));
    await u.click(screen.getByRole("switch", { name: /Give everyone/ }));
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")).toHaveLength(1));
    expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")[0]!.body).toEqual({ enabled: false, ownProviders: false, ownMcpServers: false, maxAgents: 5, maxRunning: 3, monthlyBudgetUsd: null });
  });

  it("is read-only for auditors", async () => {
    mockApi({ ...shellRoutes("platform_auditor", null), "GET /v1/admin/settings/personal-workspaces": () => settings() });
    renderApp("/admin/settings");
    expect(await screen.findByRole("switch", { name: /Give everyone/ })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("textbox", { name: "Max agents" })).toBeDisabled();
    expect(screen.getByText("You can view these settings. Only platform admins can change them.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save settings" })).toBeNull();
  });
});

describe("Admin › Teams › a personal workspace", () => {
  const adminRoutes = (role: Schemas["PlatformRole"], extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes(role, null),
    ...personalRoutes(null),
    [`GET /v1/admin/teams/${personalTeam.slug}`]: () => teamSummary({ team: personalTeam, memberCount: 1, ownerCount: 1 }),
    [`GET ${personalPath}/limits`]: () => limits(),
    "GET /v1/admin/settings/personal-workspaces": () => settings(),
    ...extra,
  });

  it("shows its owner and links to its agents, usage and audit log, without members", async () => {
    mockApi(adminRoutes("platform_admin"));
    renderApp(`/admin/teams/${personalTeam.slug}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Personal" })).toBeInTheDocument();
    expect(screen.getAllByText("Personal workspace").length).toBeGreaterThan(0);
    expect(await screen.findByText("Una User")).toBeInTheDocument();
    const tabs = screen.getByRole("tablist", { name: "Team sections" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "Limits", "Agents", "Settings"]);
    const audit = screen.getByRole("region", { name: "Audit this workspace" });
    expect(within(audit).getByRole("link", { name: "Agents" })).toHaveAttribute("href", `/teams/${personalTeam.slug}/agents`);
    expect(within(audit).getByRole("link", { name: "Usage" })).toHaveAttribute("href", `/teams/${personalTeam.slug}/usage`);
    expect(within(audit).getByRole("link", { name: "Audit log" })).toHaveAttribute("href", `/teams/${personalTeam.slug}/settings?tab=audit`);
    expect(screen.queryByRole("button", { name: /Assign owner/ })).toBeNull();
  });

  it("shows each limit's default and saves an override, with null for the defaults", async () => {
    const u = userEvent.setup();
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`PUT /v1/admin/teams/${personalTeam.slug}/limits`]: () => limits({ maxAgents: 8, overridden: ["maxAgents"] }),
      }),
    );
    renderApp(`/admin/teams/${personalTeam.slug}?tab=limits`);
    const maxAgents = await screen.findByRole("textbox", { name: /Max agents/ });
    expect(maxAgents).toBeDisabled();
    expect(maxAgents).toHaveValue("5");
    expect(await screen.findByText("Default (5)")).toBeInTheDocument();
    expect(screen.getByText("Default (2)")).toBeInTheDocument();
    expect(screen.getByText("Default (none)")).toBeInTheDocument();
    const useDefault = screen.getAllByRole("checkbox", { name: "Use the default" });
    expect(useDefault).toHaveLength(3);
    await u.click(useDefault[0]!);
    expect(maxAgents).toBeEnabled();
    await u.clear(maxAgents);
    await u.type(maxAgents, "8");
    await u.click(screen.getByRole("button", { name: "Save limits" }));
    await waitFor(() => expect(made(calls, "PUT", `/v1/admin/teams/${personalTeam.slug}/limits`)).toHaveLength(1));
    const put = made(calls, "PUT", `/v1/admin/teams/${personalTeam.slug}/limits`)[0]!;
    expect(put.headers.get("If-Match")).toBe(`"${personalTeam.revision}"`);
    expect(put.body).toEqual({ maxAgents: 8, maxRunning: null, monthlyBudgetUsd: null });
  });

  it("sends null when an override goes back to the default", async () => {
    const u = userEvent.setup();
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`GET ${personalPath}/limits`]: () => limits({ maxRunning: 4, overridden: ["maxRunning"] }),
        [`PUT /v1/admin/teams/${personalTeam.slug}/limits`]: () => limits(),
      }),
    );
    renderApp(`/admin/teams/${personalTeam.slug}?tab=limits`);
    expect(await screen.findByRole("textbox", { name: /Max running at once/ })).toHaveValue("4");
    expect(screen.getByText("This workspace's own")).toBeInTheDocument();
    await u.click(screen.getAllByRole("checkbox", { name: "Use the default" })[1]!);
    await u.click(screen.getByRole("button", { name: "Save limits" }));
    await waitFor(() => expect(made(calls, "PUT", `/v1/admin/teams/${personalTeam.slug}/limits`)).toHaveLength(1));
    expect(made(calls, "PUT", `/v1/admin/teams/${personalTeam.slug}/limits`)[0]!.body).toEqual({ maxAgents: null, maxRunning: null, monthlyBudgetUsd: null });
  });

  it("filters the teams list by kind on the server and badges personal workspaces", async () => {
    const calls = mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/teams": () => page([teamSummary({ team: personalTeam, memberCount: 1 })]),
    });
    renderApp("/admin/teams?kind=personal");
    const table = await screen.findByRole("table", { name: "Teams" });
    const row = (await within(table).findByRole("link", { name: "Personal" })).closest("tr")!;
    expect(within(row).getByText("Personal", { selector: "span" })).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.url === "/v1/admin/teams" && c.search.get("kind") === "personal")).toBe(true));
    expect(screen.getByRole("button", { name: "Personal workspaces" })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("a personal workspace in the workspace", () => {
  it("lists Personal first in the team switcher, apart from shared teams", async () => {
    const u = userEvent.setup();
    mockApi({ ...shellRoutes(), ...personalRoutes("owner"), "GET /v1/me": () => ({ ...meFor(), teams: [myTeam("editor"), myPersonal] }) });
    renderApp(`/teams/${team.slug}`);
    await screen.findByRole("heading", { level: 1, name: team.name });
    await u.click(screen.getByRole("button", { name: /Current workspace/ }));
    const items = (await screen.findAllByRole("menuitem")).map((i) => i.textContent);
    expect(items[0]).toContain("Personal");
    expect(items[1]).toContain(team.name);
  });

  it("shows only the overview, agents, models, MCP servers, usage and settings, with the limits and no members", async () => {
    mockApi({ ...shellRoutes(), ...personalRoutes("owner"), "GET /v1/me": () => ({ ...meFor(), teams: [myPersonal] }) });
    const { router } = renderApp(`/teams/${personalTeam.slug}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Personal" })).toBeInTheDocument();
    // Someone with only a personal workspace has a workspace, not "No team yet".
    expect(screen.getByRole("button", { name: /Current workspace:\s*Personal/ })).toHaveTextContent("Your personal workspace");
    const nav = screen.getByRole("navigation", { name: "Main" });
    await waitFor(() =>
      expect(within(nav).getAllByRole("link").map((l) => l.textContent)).toEqual(["Home", "All agents", "Catalog", "Overview", "Agents", "Models", "MCP servers", "Usage", "Settings"]),
    );
    expect(await screen.findByText("2 of 5 agents")).toBeInTheDocument();
    expect(screen.getByText("1 of 2 running at once")).toBeInTheDocument();
    expect(screen.getByText("Monthly budget $12.50 (not enforced yet)")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Add member" })).toBeNull();

    await router.navigate({ to: "/teams/$team/settings", params: { team: personalTeam.slug } });
    const tabs = await screen.findByRole("tablist", { name: "Team settings sections" });
    expect(within(tabs).queryByRole("tab", { name: "Members" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Leave team" })).toBeNull();
  });
});

describe("platform staff auditing a personal workspace", () => {
  it("open its agents read-only, without the test chat", async () => {
    const own = agent({ teamId: personalTeam.id, teamSlug: personalTeam.slug, teamName: "Personal", running: true });
    mockApi({
      ...shellRoutes("platform_auditor", null),
      ...personalRoutes(null),
      [`GET ${personalPath}/agents`]: () => [own],
      [`GET /v1/agents/${agentId}`]: () => own,
    });
    renderApp(`/teams/${personalTeam.slug}/agents/${agentId}?tab=test`);
    expect(await screen.findByRole("heading", { level: 1, name: "Helper" })).toBeInTheDocument();
    expect(screen.getByText("You're viewing this agent as platform staff")).toBeInTheDocument();
    const tabs = screen.getByRole("tablist", { name: "Agent sections" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Overview", "Settings"]);
    expect(screen.queryByRole("button", { name: /Hibernate/ })).toBeNull();
  });
});

describe("Admin › Models: personal workspaces", () => {
  it("sends personal from the model form", async () => {
    const u = userEvent.setup();
    const m = model();
    const calls = mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/model-provider-kinds": () => providerKinds,
      [`GET /v1/admin/model-providers/${providerId}`]: () => modelProvider(),
      [`GET /v1/admin/model-providers/${providerId}/models`]: () => [m],
      [`PATCH /v1/admin/models/${m.id}`]: (body) => model({ ...(body as object), revision: 2 }),
    });
    renderApp(`/admin/models/providers/${providerId}?form=${m.id}`);
    const box = await screen.findByRole("checkbox", { name: "Also available in personal workspaces" });
    expect(box).toBeChecked();
    await u.click(box);
    await u.click(screen.getByRole("button", { name: "Save model" }));
    await waitFor(() => expect(made(calls, "PATCH", `/v1/admin/models/${m.id}`)).toHaveLength(1));
    expect(made(calls, "PATCH", `/v1/admin/models/${m.id}`)[0]!.body).toMatchObject({ visibility: "all", personal: false });
  });

  it("says Personal workspaces in the Published to column", async () => {
    mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/admin/model-provider-kinds": () => providerKinds,
      "GET /v1/admin/model-providers": () => [modelProvider()],
      "GET /v1/admin/models": () => [model({ visibility: "teams", teams: [], personal: true })],
    });
    renderApp("/admin/models?tab=models");
    expect(await screen.findByText("Personal workspaces")).toBeInTheDocument();
  });
});
