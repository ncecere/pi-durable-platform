/* Team › MCP servers: the team's own servers and those it may use, adding a server, and a team server's page (no access mode, no publishing). */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { meFor, myTeam, platformMcpServer, team, teamMcpServer, teamServerId } from "./fixtures";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const teamPath = `/v1/teams/${team.slug}`;
const ownPath = `${teamPath}/mcp-servers/own`;
const serverPath = `${ownPath}/${teamServerId}`;

const limits = (canAddMcpServers: boolean, kind: "team" | "personal" = "team"): Schemas["WorkspaceLimits"] => ({
  kind,
  overridden: [],
  agentCount: 0,
  runningCount: 0,
  canAddProviders: false,
  canAddMcpServers,
});

const teamRoutes = (role: Schemas["TeamRole"] = "owner", canAdd = true, extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes("none", role),
  [`GET ${ownPath}`]: () => [teamMcpServer()],
  [`GET ${teamPath}/mcp-servers`]: () => [teamMcpServer(), platformMcpServer()],
  [`GET ${teamPath}/limits`]: () => limits(canAdd),
  [`GET ${serverPath}`]: () => teamMcpServer(),
  ...extra,
});

describe("Team › MCP servers", () => {
  it("shows the team's servers (no Access column) and, on Available, every server it may use with its source", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes());
    const { router, container } = renderApp(`/teams/${team.slug}/mcp-servers`);
    expect(await screen.findByRole("heading", { level: 1, name: "MCP servers" })).toBeInTheDocument();
    const own = await screen.findByRole("table", { name: "MCP servers" });
    const row = (await within(own).findByText("Team Tickets")).closest("tr")!;
    expect(within(own).queryByRole("columnheader", { name: /Access/ })).toBeNull();
    expect(within(own).getByRole("columnheader", { name: /Last read/ })).toBeInTheDocument();
    expect(within(row).getByText("2 tools")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "Team Tickets" })).toHaveAttribute("href", `/teams/${team.slug}/mcp-servers/${teamServerId}`);
    expect(screen.getByRole("button", { name: "Add server" })).toBeInTheDocument();

    await u.click(within(screen.getByRole("tablist", { name: "MCP server catalog sections" })).getByRole("tab", { name: /Available/ }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "available" }));
    const table = await screen.findByRole("table", { name: "MCP servers the team may use" });
    const mine = (await within(table).findByText("Team Tickets")).closest("tr")!;
    expect(within(mine).getByText("This team")).toBeInTheDocument();
    const platform = within(table).getByText("Fake Tools").closest("tr")!;
    expect(within(platform).getByText("Platform")).toBeInTheDocument();
    // A platform server isn't the team's to open.
    expect(within(platform).queryByRole("link", { name: "Fake Tools" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add server" })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("adds a server without an access mode or publishing, shows a taken slug and a refused address, then opens it", async () => {
    const u = userEvent.setup();
    let reply: () => Reply = () => Reply.error(409, "mcp_server_name_taken", "Another MCP server already uses the slug tickets");
    const created = teamMcpServer({ id: "new-server", name: "Tickets", slug: "tickets_two" });
    const calls = mockApi(
      teamRoutes("owner", true, {
        [`POST ${ownPath}`]: () => reply(),
        [`GET ${ownPath}/new-server`]: () => created,
      }),
    );
    const { router } = renderApp(`/teams/${team.slug}/mcp-servers`);
    await u.click(await screen.findByRole("button", { name: "Add server" }));
    const dialog = await screen.findByRole("dialog", { name: "Add MCP server" });
    expect(within(dialog).queryByRole("radio")).toBeNull();
    expect(within(dialog).queryByRole("checkbox", { name: /personal workspaces/ })).toBeNull();
    expect(within(dialog).getByText(/Agents reach it through the platform/)).toBeInTheDocument();
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Tickets");
    const url = within(dialog).getByRole("textbox", { name: "URL" });
    await u.type(url, "http://10.1.2.3/mcp");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    // A team's server needs https.
    expect(await within(dialog).findByText("Enter an address starting with https://.")).toBeInTheDocument();
    expect(made(calls, "POST", ownPath)).toHaveLength(0);

    await u.clear(url);
    await u.type(url, "https://10.1.2.3/mcp");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    // Slugs are unique across every owner: the server's message says which.
    expect(await within(dialog).findByText("Another MCP server already uses the slug tickets")).toBeInTheDocument();
    expect(made(calls, "POST", ownPath)[0]!.body).toEqual({ name: "Tickets", slug: "tickets", url: "https://10.1.2.3/mcp" });

    reply = () => Reply.error(422, "address_not_allowed", "The server's address is on a network teams can't use. Ask a platform admin to allow it.");
    const slug = within(dialog).getByRole("textbox", { name: "Slug" });
    await u.clear(slug);
    await u.type(slug, "tickets_two");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    expect(await within(dialog).findByText(/on a network teams can't use/)).toBeInTheDocument();
    expect(url).toHaveAttribute("aria-invalid", "true");

    reply = () => new Reply(201, { data: created });
    await u.clear(url);
    await u.type(url, "https://tickets.example.com/mcp");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/teams/${team.slug}/mcp-servers/new-server`));
    const body = made(calls, "POST", ownPath)[2]!.body;
    for (const key of ["accessMode", "visibility", "teams", "personal"]) expect(body).not.toHaveProperty(key);
  });

  it("says why an owner can't add a server when platform admins turned it off", async () => {
    mockApi(teamRoutes("owner", false));
    renderApp(`/teams/${team.slug}/mcp-servers`);
    expect(await screen.findByText("Adding MCP servers is turned off")).toBeInTheDocument();
    expect(screen.getByText(/Platform admins have turned off team MCP servers/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add server" })).toBeNull();
  });

  it("says personal workspaces aren't allowed servers, in a personal workspace", async () => {
    const personal: Schemas["Team"] = { ...team, kind: "personal", name: "Personal" };
    mockApi({
      ...teamRoutes("owner", false),
      "GET /v1/me": () => ({ ...meFor(), teams: [myTeam("owner", { kind: "personal", name: "Personal" })] }),
      [`GET ${teamPath}`]: (): Schemas["TeamView"] => ({ team: personal, role: "owner" }),
      [`GET ${teamPath}/limits`]: () => limits(false, "personal"),
    });
    renderApp(`/teams/${team.slug}/mcp-servers`);
    expect(await screen.findByText(/haven't let personal workspaces add their own MCP servers/)).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "MCP servers" })).toHaveAttribute("href", `/teams/${team.slug}/mcp-servers`);
  });

  it("is read-only for members and editors: no Add server, and the server's page has no actions", async () => {
    for (const role of ["member", "editor"] as const) {
      mockApi(teamRoutes(role, false));
      const { unmount } = renderApp(`/teams/${team.slug}/mcp-servers`);
      expect(await screen.findByText(/Only its owners and admins can change them/)).toBeInTheDocument();
      expect(screen.queryByText("Adding MCP servers is turned off")).toBeNull();
      expect(screen.queryByRole("button", { name: "Add server" })).toBeNull();
      unmount();
    }
  });

  it("lets members read a server's settings and tools without changing them", async () => {
    mockApi(teamRoutes("member", false));
    renderApp(`/teams/${team.slug}/mcp-servers/${teamServerId}?tab=settings`);
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Replace headers" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete MCP server" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Refresh tools" })).toBeNull();
  });
});

describe("Team › MCP servers › a server", () => {
  it("has no access mode or publishing, saves with If-Match through the team's route, and shows a refused address", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes("admin", true, { [`PATCH ${serverPath}`]: () => Reply.error(422, "address_unresolved", "The server's host name could not be resolved") }));
    renderApp(`/teams/${team.slug}/mcp-servers/${teamServerId}?tab=settings`);
    const url = await screen.findByRole("textbox", { name: "URL" });
    expect(screen.queryByRole("radiogroup", { name: "How agents reach it" })).toBeNull();
    expect(screen.queryByRole("radiogroup", { name: "Published to" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: /personal workspaces/ })).toBeNull();
    expect(screen.getByText(/Agents reach it through the platform/)).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Slug" })).toBeDisabled();
    await u.clear(url);
    await u.type(url, "https://nowhere.example/mcp");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", serverPath)).toHaveLength(1));
    const patch = made(calls, "PATCH", serverPath)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"2"');
    expect(patch.body).toEqual({
      name: "Team Tickets",
      description: "The team's ticketing",
      url: "https://nowhere.example/mcp",
      enabled: true,
      sendIdentity: true,
      identityAudience: "",
      identityEmail: false,
    });
    expect(await screen.findByText("The server's host name could not be resolved")).toBeInTheDocument();
    expect(url).toHaveAttribute("aria-invalid", "true");
  });

  it("refreshes the tools through the team's route, and the breadcrumb names the server", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes("owner", true, { [`POST ${serverPath}/refresh`]: () => teamMcpServer({ revision: 3 }) }));
    renderApp(`/teams/${team.slug}/mcp-servers/${teamServerId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Team Tickets" })).toBeInTheDocument();
    expect(screen.getByText("Platform Tools only")).toBeInTheDocument();
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    await waitFor(() => expect(within(crumbs).getByRole("link", { name: "MCP servers" })).toHaveAttribute("href", `/teams/${team.slug}/mcp-servers`));
    await u.click(screen.getByRole("button", { name: "Refresh tools" }));
    await waitFor(() => expect(made(calls, "POST", `${serverPath}/refresh`)).toHaveLength(1));
    expect(within(screen.getByRole("table", { name: "Tools of Team Tickets" })).getByText("open_ticket")).toBeInTheDocument();
  });

  it("deletes it through the team's route and goes back to the list", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes("owner", true, { [`DELETE ${serverPath}`]: () => ({}) }));
    const { router } = renderApp(`/teams/${team.slug}/mcp-servers/${teamServerId}?tab=settings`);
    await u.click(await screen.findByRole("button", { name: "Delete MCP server" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete Team Tickets?" });
    await u.click(within(ask).getByRole("button", { name: "Delete MCP server" }));
    await waitFor(() => expect(made(calls, "DELETE", serverPath)).toHaveLength(1));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/teams/${team.slug}/mcp-servers`));
  });

  it("shows the not-found page for a server that isn't the team's", async () => {
    mockApi(teamRoutes("owner", true, { [`GET ${ownPath}/elsewhere`]: () => Reply.error(404, "not_found", "Not found") }));
    renderApp(`/teams/${team.slug}/mcp-servers/elsewhere`);
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
  });
});

describe("Team › MCP servers in the command palette", () => {
  it("jumps to the team's MCP servers", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes());
    const { router } = renderApp(`/teams/${team.slug}`);
    await screen.findByRole("heading", { level: 1, name: team.name });
    await u.keyboard("{Control>}k{/Control}");
    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    await u.type(within(dialog).getByRole("combobox", { name: "Command palette" }), "model context protocol");
    await u.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe(`/teams/${team.slug}/mcp-servers`));
  });
});
