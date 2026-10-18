/*
 * Team MCP servers seen by platform admins (Admin › MCP servers' Owner column and filter, a team's server in
 * Admin, Admin › Settings' switches) and where servers are attached (the template MCP picker, the catalog).
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { model, page, platformMcpServer, team, teamMcpServer, teamServerId } from "./fixtures";
import { trustedClients } from "./fixtures-oauth";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const byOwner = (o: string | null) => [platformMcpServer(), teamMcpServer()].filter((m) => !o || (o === "platform" ? !m.team : m.team?.slug === o));

const adminRoutes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/mcp-servers": (_, call) => byOwner(call.search.get("owner")),
  [`GET /v1/admin/mcp-servers/${teamServerId}`]: () => teamMcpServer(),
  ...extra,
});

describe("Admin › MCP servers › owners", () => {
  it("shows each server's owner, and the Owner filter goes in ?owner= and to the server", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes());
    const { router } = renderApp("/admin/mcp-servers");
    const table = await screen.findByRole("table", { name: "MCP servers" });
    const row = (await within(table).findByText("Team Tickets")).closest("tr")!;
    expect(within(row).getByRole("link", { name: team.name })).toHaveAttribute("href", `/admin/teams/${team.slug}`);
    expect(within(row).getByText(`${team.name} only`)).toBeInTheDocument();
    expect(within(within(table).getByText("Fake Tools").closest("tr")!).getByText("Platform")).toBeInTheDocument();

    const filter = screen.getByRole("combobox", { name: "Owner" });
    await waitFor(() => expect(within(filter).getByRole("option", { name: team.name })).toBeInTheDocument());
    await u.selectOptions(filter, team.slug);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ owner: team.slug }));
    await waitFor(() => expect(within(table).queryByText("Fake Tools")).toBeNull());
    expect(calls.some((c) => c.url === "/v1/admin/mcp-servers" && c.search.get("owner") === team.slug)).toBe(true);

    await u.selectOptions(screen.getByRole("combobox", { name: "Owner" }), "platform");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ owner: "platform" }));
    expect(await within(table).findByText("Fake Tools")).toBeInTheDocument();
    await waitFor(() => expect(within(table).queryByText("Team Tickets")).toBeNull());
    await u.selectOptions(screen.getByRole("combobox", { name: "Owner" }), "all");
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty("owner"));
  });

  it("opens a team's server with its owner, without access mode or publishing; admins may disable and delete it", async () => {
    const u = userEvent.setup();
    const path = `/v1/admin/mcp-servers/${teamServerId}`;
    let stored = teamMcpServer();
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`GET ${path}`]: () => stored,
        [`PATCH ${path}`]: (body) => (stored = teamMcpServer({ ...(body as object), revision: 3 })),
        [`DELETE ${path}`]: () => ({}),
      }),
    );
    const { router } = renderApp(`/admin/mcp-servers/${teamServerId}?tab=settings`);
    expect(await screen.findByRole("heading", { level: 1, name: "Team Tickets" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: team.name })[0]).toHaveAttribute("href", `/admin/teams/${team.slug}`);
    expect(screen.queryByRole("radiogroup", { name: "How agents reach it" })).toBeNull();
    expect(screen.queryByRole("radiogroup", { name: "Published to" })).toBeNull();
    await u.click(await screen.findByRole("switch", { name: "Enabled" }));
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", path)).toHaveLength(1));
    const patch = made(calls, "PATCH", path)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"2"');
    expect(patch.body).toMatchObject({ enabled: false });
    for (const key of ["accessMode", "visibility", "teams", "personal"]) expect(patch.body).not.toHaveProperty(key);

    await u.click(screen.getByRole("button", { name: "Delete MCP server" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete Team Tickets?" });
    await u.click(within(ask).getByRole("button", { name: "Delete MCP server" }));
    await waitFor(() => expect(made(calls, "DELETE", path)).toHaveLength(1));
    await waitFor(() => expect(router.state.location.pathname).toBe("/admin/mcp-servers"));
  });
});

describe("Admin › Settings › team MCP servers", () => {
  const path = "/v1/admin/settings/models";
  const settings = (extra: Partial<Schemas["ModelSettings"]> = {}): Schemas["ModelSettings"] => ({
    teamsMayAddProviders: true,
    teamsMayAddMcpServers: true,
    privateNetworks: [],
    revision: 9,
    updatedAt: "2026-10-18T10:00:00Z",
    ...extra,
  });
  const personal: Schemas["PersonalWorkspaceSettings"] = {
    enabled: true,
    ownProviders: true,
    ownMcpServers: false,
    maxAgents: 5,
    maxRunning: 2,
    monthlyBudgetUsd: null,
    revision: 7,
    updatedAt: "2026-10-18T10:00:00Z",
  };
  const routes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes(role, null),
    "GET /v1/admin/settings/personal-workspaces": () => personal,
    "GET /v1/admin/settings/oauth-clients": () => trustedClients(),
    [`GET ${path}`]: () => settings(),
    ...extra,
  });

  it("saves “Teams may add their own MCP servers” with If-Match, and says the private networks cover both", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { [`PUT ${path}`]: (body) => settings({ ...(body as object), revision: 10 }) }));
    renderApp("/admin/settings");
    expect(await screen.findByRole("heading", { name: "Team providers and MCP servers" })).toBeInTheDocument();
    const toggle = await screen.findByRole("switch", { name: "Teams may add their own MCP servers" });
    expect(toggle).toBeChecked();
    expect(screen.getByText(/Teams' own providers and MCP servers must be on public addresses/)).toBeInTheDocument();
    await u.click(toggle);
    await u.click(screen.getByRole("button", { name: "Save team provider and MCP server settings" }));
    await waitFor(() => expect(made(calls, "PUT", path)).toHaveLength(1));
    const put = made(calls, "PUT", path)[0]!;
    expect(put.headers.get("If-Match")).toBe('"9"');
    expect(put.body).toEqual({ teamsMayAddProviders: true, teamsMayAddMcpServers: false, privateNetworks: [] });
  });

  it("lets people add MCP servers in their personal workspace (ownMcpServers), saved with If-Match", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { "PUT /v1/admin/settings/personal-workspaces": (body) => ({ ...personal, ...(body as object), revision: 8 }) }));
    renderApp("/admin/settings");
    const own = await screen.findByRole("switch", { name: "People may add their own MCP servers in their personal workspace" });
    expect(own).not.toBeChecked();
    expect(screen.getByText(/also needs “Teams may add their own MCP servers” turned on/)).toBeInTheDocument();
    await u.click(own);
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")).toHaveLength(1));
    const put = made(calls, "PUT", "/v1/admin/settings/personal-workspaces")[0]!;
    expect(put.headers.get("If-Match")).toBe('"7"');
    expect(put.body).toMatchObject({ ownProviders: true, ownMcpServers: true });
  });

  it("is read-only for auditors", async () => {
    mockApi(routes("platform_auditor"));
    renderApp("/admin/settings");
    expect(await screen.findByRole("switch", { name: "Teams may add their own MCP servers" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("switch", { name: "People may add their own MCP servers in their personal workspace" })).toHaveAttribute("aria-disabled", "true");
  });
});

describe("attaching a team's own MCP server", () => {
  const templateId = "27272727-2727-4272-8272-272727272727";
  const template = (extra: Partial<Schemas["AgentTemplate"]> = {}): Schemas["AgentTemplate"] => ({
    id: templateId,
    slug: "helper",
    name: "Helper",
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
    ...extra,
  });
  const version: Schemas["AgentTemplateVersion"] = { version: 1, instructions: "Be brief.", notes: "Initial version", mcp: [], createdAt: "2026-10-01T10:00:00Z" };
  const teamRoutes = (extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes("none", "owner"),
    [`GET /v1/teams/${team.slug}/agent-templates`]: () => [template()],
    [`GET /v1/teams/${team.slug}/models`]: () => [],
    [`GET /v1/teams/${team.slug}/mcp-servers`]: () => [teamMcpServer(), platformMcpServer()],
    [`GET /v1/agent-templates/${templateId}`]: () => template(),
    [`GET /v1/agent-templates/${templateId}/versions`]: () => [version],
    ...extra,
  });

  it("offers a team template its team's own servers, badged “This team”, and the platform's", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes({ [`POST /v1/teams/${team.slug}/agent-templates`]: () => new Reply(201, { data: template() }) }));
    renderApp(`/teams/${team.slug}/templates`);
    await u.click(await screen.findByRole("button", { name: "New template" }));
    const dialog = await screen.findByRole("dialog", { name: "New team template" });
    const own = await within(dialog).findByRole("checkbox", { name: /Team Tickets/ });
    expect(own).toHaveAccessibleName("Team Tickets This team");
    expect(within(dialog).getByRole("checkbox", { name: "Fake Tools" })).toBeInTheDocument();
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Helper");
    await u.type(within(dialog).getByRole("textbox", { name: "Slug" }), "helper");
    await u.type(within(dialog).getByRole("textbox", { name: "Instructions" }), "Be brief.");
    await u.click(own);
    await u.click(within(dialog).getByRole("button", { name: "Create template" }));
    await waitFor(() => expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)).toHaveLength(1));
    expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)[0]!.body).toMatchObject({ mcp: [{ serverId: teamServerId }] });
  });

  it("offers a platform template only the platform's servers (?owner=platform)", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...adminRoutes(),
      "GET /v1/admin/models": () => [model()],
      "GET /v1/admin/agent-templates": () => [],
      "GET /v1/admin/audit": () => page([]),
    });
    renderApp("/admin/templates");
    await u.click(await screen.findByRole("button", { name: "New platform template" }));
    const dialog = await screen.findByRole("dialog", { name: "New platform template" });
    expect(await within(dialog).findByRole("checkbox", { name: "Fake Tools" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("checkbox", { name: /Team Tickets/ })).toBeNull();
    expect(calls.some((c) => c.url === "/v1/admin/mcp-servers" && c.search.get("owner") === "platform")).toBe(true);
  });

  it("offers a catalog template's new version platform servers only, and explains 409 team_mcp_server", async () => {
    const u = userEvent.setup();
    mockApi(
      teamRoutes({
        [`GET /v1/agent-templates/${templateId}`]: () => template({ status: "published" }),
        [`POST /v1/agent-templates/${templateId}/versions`]: () => Reply.error(409, "team_mcp_server", "That MCP server is a team's own, which only that team can use"),
      }),
    );
    renderApp(`/templates/${templateId}?tab=versions`);
    await u.click(await screen.findByRole("button", { name: "New version" }));
    const dialog = await screen.findByRole("dialog", { name: "New version of Helper" });
    await u.click(await within(dialog).findByRole("checkbox", { name: "Fake Tools" }));
    expect(within(dialog).queryByRole("checkbox", { name: /Team Tickets/ })).toBeNull();
    expect(within(dialog).getByText(/Templates in the catalog get platform MCP servers only/)).toBeInTheDocument();
    await u.click(within(dialog).getByRole("button", { name: "Save v2" }));
    expect((await screen.findAllByText(/can't use the team's own MCP servers/))[0]).toBeInTheDocument();
  });

  it("says why a template using a team's own server can't go to the catalog", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes({ [`POST /v1/agent-templates/${templateId}/publish`]: () => Reply.error(409, "team_mcp_server", "That MCP server is a team's own") }));
    renderApp(`/templates/${templateId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Helper" })).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "More actions" }));
    await u.click(await screen.findByRole("menuitem", { name: "Submit to the catalog" }));
    expect((await screen.findAllByText("Couldn't submit it to the catalog"))[0]).toBeInTheDocument();
    expect(screen.getAllByText(/uses one of the team's own MCP servers, which only the team can use/)[0]).toBeInTheDocument();
  });
});
