/* Admin › MCP servers (add one, a server's tools and refresh, delete) and the MCP servers of templates (create, new version). */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { team } from "./fixtures";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const serverId = "21212121-2121-4212-8212-212121212121";
const serverPath = `/v1/admin/mcp-servers/${serverId}`;

const mcpServer = (extra: Partial<Schemas["McpServer"]> = {}): Schemas["McpServer"] => ({
  id: serverId,
  name: "Fake Tools",
  slug: "fake_tools",
  description: "Echo and search",
  url: "https://mcp.example.com/mcp",
  accessMode: "proxy",
  enabled: true,
  visibility: "all",
  personal: true,
  teams: null,
  hasHeaders: true,
  tools: [
    { name: "echo", title: "Echo", description: "Repeats the text" },
    { name: "search", description: "Searches the docs" },
    { name: "delete_all", description: "Deletes everything" },
  ],
  toolsRefreshedAt: "2026-10-01T10:00:00Z",
  toolsError: null,
  revision: 3,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
  ...extra,
});

const adminRoutes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/mcp-servers": () => [mcpServer()],
  [`GET ${serverPath}`]: () => mcpServer(),
  ...extra,
});

describe("Admin › MCP servers › Add MCP server", () => {
  it("prefills the slug from the name and sends the name, slug, URL, headers and access mode", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes("platform_admin", { "POST /v1/admin/mcp-servers": () => new Reply(201, { data: mcpServer() }) }));
    const { router } = renderApp("/admin/mcp-servers");
    expect(await screen.findByRole("heading", { level: 1, name: "MCP servers" })).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "MCP servers" })).findByText("Fake Tools")).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Add MCP server" }));
    const dialog = await screen.findByRole("dialog", { name: "Add MCP server" });
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Fake Tools (Staging)!");
    const slug = within(dialog).getByRole("textbox", { name: "Slug" });
    expect(slug).toHaveValue("fake_tools_staging");
    expect(within(dialog).getByText(/Tools appear as mcp__fake_tools_staging__/)).toBeInTheDocument();
    await u.clear(slug);
    await u.type(slug, "fake_tools");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    expect(await within(dialog).findByText("Enter the server's address.")).toBeInTheDocument();
    expect(made(calls, "POST", "/v1/admin/mcp-servers")).toHaveLength(0);

    await u.type(within(dialog).getByRole("textbox", { name: "URL" }), "https://mcp.example.com/mcp");
    await u.click(within(dialog).getByRole("radio", { name: /Direct/ }));
    await u.click(within(dialog).getByRole("button", { name: /Advanced/ }));
    await u.click(within(dialog).getByRole("button", { name: "Add header" }));
    await u.type(within(dialog).getByRole("textbox", { name: "Header 1 name" }), "Authorization");
    await u.type(within(dialog).getByRole("textbox", { name: "Header 1 value" }), "Bearer secret-token");
    await u.click(within(dialog).getByRole("button", { name: "Add MCP server" }));
    await waitFor(() => expect(made(calls, "POST", "/v1/admin/mcp-servers")).toHaveLength(1));
    expect(made(calls, "POST", "/v1/admin/mcp-servers")[0]!.body).toEqual({
      name: "Fake Tools (Staging)!",
      slug: "fake_tools",
      url: "https://mcp.example.com/mcp",
      accessMode: "direct",
      visibility: "all",
      personal: true,
      headers: { Authorization: "Bearer secret-token" },
    });
    await waitFor(() => expect(router.state.location.pathname).toBe(`/admin/mcp-servers/${serverId}`));
    // Header values are never shown again.
    expect(document.body.textContent).not.toContain("secret-token");
  });

  it("marks a server whose tools couldn't be read, and lets auditors read the list without adding", async () => {
    mockApi(adminRoutes("platform_auditor", { "GET /v1/admin/mcp-servers": () => [mcpServer({ toolsError: "connection refused" })] }));
    renderApp("/admin/mcp-servers");
    const table = await screen.findByRole("table", { name: "MCP servers" });
    expect(await within(table).findByText("Tools couldn't be read")).toHaveAttribute("title", "connection refused");
    expect(screen.queryByRole("button", { name: "Add MCP server" })).toBeNull();
  });
});

describe("Admin › MCP servers › a server", () => {
  it("shows why the tools couldn't be read, and Refresh tools reads them again", async () => {
    const u = userEvent.setup();
    let stored = mcpServer({ toolsError: "The server answered 401: bad token", tools: [] });
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`GET ${serverPath}`]: () => stored,
        [`POST ${serverPath}/refresh`]: () => (stored = mcpServer({ revision: 4 })),
      }),
    );
    renderApp(`/admin/mcp-servers/${serverId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Fake Tools" })).toBeInTheDocument();
    expect(screen.getByText("The tools couldn't be read the last time")).toBeInTheDocument();
    expect(screen.getByText(/The server answered 401: bad token/)).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Refresh tools" }));
    await waitFor(() => expect(made(calls, "POST", `${serverPath}/refresh`)).toHaveLength(1));
    expect(await within(screen.getByRole("table", { name: "Tools of Fake Tools" })).findByText("echo")).toBeInTheDocument();
    expect(screen.queryByText("The tools couldn't be read the last time")).toBeNull();
  });

  it("is read-only for auditors: no refresh, no edits", async () => {
    mockApi(adminRoutes("platform_auditor"));
    renderApp(`/admin/mcp-servers/${serverId}?tab=settings`);
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Replace headers" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete MCP server" })).toBeNull();
    expect(screen.getByText("Extra headers are set. Their values aren't shown.")).toBeInTheDocument();
  });

  it("saves with If-Match, sends headers only when replaced, and surfaces mcp_server_in_use on delete", async () => {
    const u = userEvent.setup();
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`PATCH ${serverPath}`]: () => mcpServer({ enabled: false, revision: 4 }),
        [`DELETE ${serverPath}`]: () => Reply.error(409, "mcp_server_in_use", "Template versions use this MCP server: disable it instead"),
      }),
    );
    renderApp(`/admin/mcp-servers/${serverId}?tab=settings`);
    await u.click(await screen.findByRole("switch", { name: "Enabled" }));
    await u.click(screen.getByRole("checkbox", { name: "Also available in personal workspaces" }));
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", serverPath)).toHaveLength(1));
    const patch = made(calls, "PATCH", serverPath)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"3"');
    expect(patch.body).toEqual({
      name: "Fake Tools",
      description: "Echo and search",
      url: "https://mcp.example.com/mcp",
      accessMode: "proxy",
      enabled: false,
      visibility: "all",
      personal: false,
    });
    await u.click(screen.getByRole("button", { name: "Delete MCP server" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete Fake Tools?" });
    await u.click(within(ask).getByRole("button", { name: "Delete MCP server" }));
    expect(await within(ask).findByText("Template versions use this MCP server: disable it instead")).toBeInTheDocument();
  });
});

describe("a template's MCP servers", () => {
  const templateId = "20202020-2020-4202-8202-202020202020";
  const template: Schemas["AgentTemplate"] = {
    id: templateId,
    slug: "helper",
    name: "Helper",
    description: "",
    teamId: team.id,
    teamSlug: team.slug,
    teamName: team.name,
    status: "private",
    archived: false,
    currentVersion: 2,
    revision: 3,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-01T10:00:00Z",
  };
  const versions: Schemas["AgentTemplateVersion"][] = [
    {
      version: 2,
      instructions: "Be brief.",
      notes: "Tools",
      mcp: [{ serverId, name: "Fake Tools", slug: "fake_tools", tools: ["echo", "search"] }],
      clientMcp: true,
      createdAt: "2026-10-02T10:00:00Z",
    },
    { version: 1, instructions: "Be helpful.", notes: "Initial version", mcp: [{ serverId, name: "Fake Tools", slug: "fake_tools" }], createdAt: "2026-10-01T10:00:00Z" },
  ];
  const teamRoutes = (extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes("none", "editor"),
    [`GET /v1/teams/${team.slug}/agent-templates`]: () => [template],
    [`GET /v1/teams/${team.slug}/models`]: () => [],
    [`GET /v1/teams/${team.slug}/mcp-servers`]: () => [mcpServer()],
    [`GET /v1/agent-templates/${templateId}`]: () => template,
    [`GET /v1/agent-templates/${templateId}/versions`]: () => versions,
    ...extra,
  });

  it("creates a template with some of a server's tools and apps' own servers allowed", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes({ [`POST /v1/teams/${team.slug}/agent-templates`]: () => new Reply(201, { data: template }) }));
    renderApp(`/teams/${team.slug}/templates`);
    await u.click(await screen.findByRole("button", { name: "New template" }));
    const dialog = await screen.findByRole("dialog", { name: "New team template" });
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Helper");
    await u.type(within(dialog).getByRole("textbox", { name: "Slug" }), "helper");
    await u.type(within(dialog).getByRole("textbox", { name: "Instructions" }), "Be brief.");
    await u.click(await within(dialog).findByRole("checkbox", { name: "Fake Tools" }));
    expect(within(dialog).getByRole("radio", { name: "All tools" })).toBeChecked();
    await u.click(within(dialog).getByRole("radio", { name: "Only these tools" }));
    await u.click(within(dialog).getByRole("button", { name: "Create template" }));
    expect(await within(dialog).findByText("Pick at least one tool, or choose All tools.")).toBeInTheDocument();
    expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)).toHaveLength(0);
    await u.click(within(dialog).getByRole("checkbox", { name: "echo" }));
    await u.click(within(dialog).getByRole("checkbox", { name: "search" }));
    await u.click(within(dialog).getByRole("checkbox", { name: /Let apps give agents their own MCP servers/ }));
    await u.click(within(dialog).getByRole("button", { name: "Create template" }));
    await waitFor(() => expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)).toHaveLength(1));
    expect(made(calls, "POST", `/v1/teams/${team.slug}/agent-templates`)[0]!.body).toMatchObject({
      slug: "helper",
      mcp: [{ serverId, tools: ["echo", "search"] }],
      clientMcp: true,
    });
  });

  it("shows each version's servers, and a new version leaves them out unless changed", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes({ [`POST /v1/agent-templates/${templateId}/versions`]: () => new Reply(201, { data: template }) }));
    renderApp(`/templates/${templateId}?tab=versions`);
    expect(await screen.findByText("Fake Tools: echo, search")).toBeInTheDocument();
    expect(screen.getByText("Fake Tools: all tools")).toBeInTheDocument();
    expect(screen.getByText("Apps may add MCP servers")).toBeInTheDocument();
    const post = () => made(calls, "POST", `/v1/agent-templates/${templateId}/versions`);

    await u.click(screen.getByRole("button", { name: "New version" }));
    let dialog = await screen.findByRole("dialog", { name: "New version of Helper" });
    await waitFor(() => expect(within(dialog).getByRole("checkbox", { name: "Fake Tools" })).toBeChecked());
    expect(within(dialog).getByRole("checkbox", { name: "echo" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "delete_all" })).not.toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: /Let apps give agents/ })).toBeChecked();
    await u.click(within(dialog).getByRole("button", { name: "Save v3" }));
    await waitFor(() => expect(post()).toHaveLength(1));
    expect(post()[0]!.body).not.toHaveProperty("mcp");
    expect(post()[0]!.body).not.toHaveProperty("clientMcp");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "New version of Helper" })).toBeNull());

    await u.click(screen.getByRole("button", { name: "New version" }));
    dialog = await screen.findByRole("dialog", { name: "New version of Helper" });
    await u.click(await within(dialog).findByRole("radio", { name: "All tools" }));
    await u.click(within(dialog).getByRole("button", { name: "Save v3" }));
    await waitFor(() => expect(post()).toHaveLength(2));
    expect(post()[1]!.body).toMatchObject({ mcp: [{ serverId }] });
    expect(post()[1]!.body).not.toHaveProperty("clientMcp");
  });
});
