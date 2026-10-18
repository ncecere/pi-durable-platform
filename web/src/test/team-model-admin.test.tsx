/*
 * Team model providers seen by platform admins (Admin › Models' Owner column and filter, a team's provider in
 * Admin, Admin › Settings' team providers and private networks) and where team models are chosen (the model
 * picker, platform templates, submitting a template to the catalog).
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { isCidr, networksProblem } from "../pages/admin/settings/networks";
import { agent, agentId, model, modelProvider, page, providerKinds, team } from "./fixtures";
import { trustedClients } from "./fixtures-oauth";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const owner: Schemas["ModelOwner"] = { id: team.id, slug: team.slug, name: team.name, kind: "team" };
const teamProviderId = "23232323-2323-4232-8232-232323232323";
const teamProvider = modelProvider({ id: teamProviderId, name: "Team vLLM", team: owner });
const teamModel = model({ id: "own-1", name: "Own One", providerId: teamProviderId, providerName: "Team vLLM", visibility: "teams", personal: false, providerTeam: owner });

const adminRoutes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/model-provider-kinds": () => providerKinds,
  "GET /v1/admin/model-providers": (_, call) => {
    const o = call.search.get("owner");
    return [modelProvider(), teamProvider].filter((p) => !o || (o === "platform" ? !p.team : p.team?.slug === o));
  },
  "GET /v1/admin/models": (_, call) => {
    const o = call.search.get("owner");
    return [model(), teamModel].filter((m) => !o || (o === "platform" ? !m.providerTeam : m.providerTeam?.slug === o));
  },
  [`GET /v1/admin/model-providers/${teamProviderId}`]: () => teamProvider,
  [`GET /v1/admin/model-providers/${teamProviderId}/models`]: () => [teamModel],
  ...extra,
});

describe("Admin › Models › owners", () => {
  it("shows each provider's and model's owner, and the Owner filter goes in ?owner= and to the server", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes());
    const { router } = renderApp("/admin/models");
    const table = await screen.findByRole("table", { name: "Providers" });
    const row = (await within(table).findByText("Team vLLM")).closest("tr")!;
    expect(within(row).getByRole("link", { name: team.name })).toHaveAttribute("href", `/admin/teams/${team.slug}`);
    expect(within(within(table).getByText("LiteLLM gateway").closest("tr")!).getByText("Platform")).toBeInTheDocument();

    const filter = screen.getByRole("combobox", { name: "Owner" });
    await waitFor(() => expect(within(filter).getByRole("option", { name: team.name })).toBeInTheDocument());
    await u.selectOptions(filter, team.slug);
    await waitFor(() => expect(router.state.location.search).toMatchObject({ owner: team.slug }));
    await waitFor(() => expect(within(table).queryByText("LiteLLM gateway")).toBeNull());
    expect(calls.some((c) => c.url === "/v1/admin/model-providers" && c.search.get("owner") === team.slug)).toBe(true);

    // The Models tab keeps the owner (and ?tab=).
    await u.click(within(screen.getByRole("tablist", { name: "Model catalog sections" })).getByRole("tab", { name: /Models/ }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "models", owner: team.slug }));
    const models = await screen.findByRole("table", { name: "Every model" });
    expect(await within(models).findByText("Own One")).toBeInTheDocument();
    expect(within(models).queryByText("Fake One")).toBeNull();
    expect(within(models).getByText(`${team.name} only`)).toBeInTheDocument();

    await u.selectOptions(screen.getByRole("combobox", { name: "Owner" }), "platform");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "models", owner: "platform" }));
    expect(await within(models).findByText("Fake One")).toBeInTheDocument();
    await u.selectOptions(screen.getByRole("combobox", { name: "Owner" }), "all");
    await waitFor(() => expect(router.state.location.search).not.toHaveProperty("owner"));
  });

  it("opens a team's provider with its owner, without access mode or publishing, and admins may delete it", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes("platform_admin", { [`DELETE /v1/admin/model-providers/${teamProviderId}`]: () => ({}) }));
    const { router } = renderApp(`/admin/models/providers/${teamProviderId}?tab=settings`);
    expect(await screen.findByRole("heading", { level: 1, name: "Team vLLM" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: team.name })[0]).toHaveAttribute("href", `/admin/teams/${team.slug}`);
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeEnabled();
    expect(screen.queryByRole("radiogroup", { name: "How agents reach it" })).toBeNull();
    await u.click(screen.getByRole("button", { name: "Delete provider" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete Team vLLM?" });
    await u.click(within(ask).getByRole("button", { name: "Delete provider" }));
    await waitFor(() => expect(made(calls, "DELETE", `/v1/admin/model-providers/${teamProviderId}`)).toHaveLength(1));
    await waitFor(() => expect(router.state.location.pathname).toBe("/admin/models"));
  });

  it("edits a team's model in Admin without publishing it", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes("platform_admin", { [`PATCH /v1/admin/models/${teamModel.id}`]: (body) => ({ ...teamModel, ...(body as object), revision: 2 }) }));
    renderApp(`/admin/models/providers/${teamProviderId}?form=${teamModel.id}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Edit Own One" })).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup", { name: "Published to" })).toBeNull();
    await u.click(screen.getByRole("switch", { name: "Enabled" }));
    await u.click(screen.getByRole("button", { name: "Save model" }));
    await waitFor(() => expect(made(calls, "PATCH", `/v1/admin/models/${teamModel.id}`)).toHaveLength(1));
    const body = made(calls, "PATCH", `/v1/admin/models/${teamModel.id}`)[0]!.body;
    expect(body).toMatchObject({ enabled: false });
    for (const key of ["visibility", "teams", "personal"]) expect(body).not.toHaveProperty(key);
  });
});

describe("Admin › Settings › Team model providers", () => {
  const path = "/v1/admin/settings/models";
  const settings = (extra: Partial<Schemas["ModelSettings"]> = {}): Schemas["ModelSettings"] => ({
    teamsMayAddProviders: true,
    privateNetworks: ["10.20.0.0/16"],
    revision: 4,
    updatedAt: "2026-10-18T10:00:00Z",
    ...extra,
  });
  const personal: Schemas["PersonalWorkspaceSettings"] = { enabled: true, ownProviders: false, maxAgents: 5, maxRunning: 2, monthlyBudgetUsd: null, revision: 7, updatedAt: "2026-10-18T10:00:00Z" };
  const routes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes(role, null),
    "GET /v1/admin/settings/personal-workspaces": () => personal,
    "GET /v1/admin/settings/oauth-clients": () => trustedClients(),
    [`GET ${path}`]: () => settings(),
    ...extra,
  });

  it("saves the switch and the private networks with If-Match, and shows the server's refusal on the field", async () => {
    const u = userEvent.setup();
    let refuse = true;
    const calls = mockApi(
      routes("platform_admin", {
        [`PUT ${path}`]: (body) =>
          refuse
            ? Reply.error(400, "invalid_private_networks", "Private networks are at most 50 private CIDRs (such as 10.20.0.0/16); loopback, link-local and public ranges can't be listed")
            : settings({ ...(body as object), revision: 5 }),
      }),
    );
    renderApp("/admin/settings");
    const toggle = await screen.findByRole("switch", { name: "Teams may add their own model providers" });
    expect(toggle).toBeChecked();
    expect(screen.getByText(/Team providers must be on public addresses/)).toBeInTheDocument();
    expect(screen.getByText("10.20.0.0/16")).toBeInTheDocument();
    const input = screen.getByRole("textbox", { name: /Private networks/ });

    // Not a network: caught before Save.
    await u.type(input, "10.0.0.1{Enter}");
    expect(await screen.findByText(/10\.0\.0\.1 isn't a network/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save team provider settings" })).toBeDisabled();
    await u.click(screen.getByRole("button", { name: "Remove network 10.0.0.1" }));

    await u.type(input, "8.8.8.0/24{Enter}");
    await u.click(toggle);
    await u.click(screen.getByRole("button", { name: "Save team provider settings" }));
    expect(await screen.findByText(/loopback, link-local and public ranges can't be listed/)).toBeInTheDocument();
    const put = made(calls, "PUT", path)[0]!;
    expect(put.headers.get("If-Match")).toBe('"4"');
    expect(put.body).toEqual({ teamsMayAddProviders: false, privateNetworks: ["10.20.0.0/16", "8.8.8.0/24"] });

    refuse = false;
    await u.click(screen.getByRole("button", { name: "Remove network 8.8.8.0/24" }));
    await u.type(input, "fd00:10::/32{Enter}");
    await u.click(screen.getByRole("button", { name: "Save team provider settings" }));
    await waitFor(() => expect(made(calls, "PUT", path)).toHaveLength(2));
    expect(made(calls, "PUT", path)[1]!.body).toEqual({ teamsMayAddProviders: false, privateNetworks: ["10.20.0.0/16", "fd00:10::/32"] });
    expect(await screen.findByText("Team model provider settings saved")).toBeInTheDocument();
  });

  it("lets people add providers in their personal workspace (ownProviders)", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { "PUT /v1/admin/settings/personal-workspaces": (body) => ({ ...personal, ...(body as object), revision: 8 }) }));
    renderApp("/admin/settings");
    const own = await screen.findByRole("switch", { name: "People may add their own model providers in their personal workspace" });
    expect(own).not.toBeChecked();
    expect(screen.getByText(/also needs “Teams may add their own model providers” turned on/)).toBeInTheDocument();
    await u.click(own);
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")).toHaveLength(1));
    expect(made(calls, "PUT", "/v1/admin/settings/personal-workspaces")[0]!.body).toMatchObject({ ownProviders: true });
  });

  it("is read-only for auditors", async () => {
    mockApi(routes("platform_auditor"));
    renderApp("/admin/settings");
    expect(await screen.findByRole("switch", { name: "Teams may add their own model providers" })).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByRole("textbox", { name: /Private networks/ })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save team provider settings" })).toBeNull();
  });

  it("checks CIDRs: IPv4 and IPv6 with a prefix length", () => {
    for (const ok of ["10.20.0.0/16", "192.168.1.0/24", "fd00::/8", "fd00:10::/32", "::ffff:10.0.0.0/104", "2001:db8:0:0:0:0:0:0/32"]) expect(isCidr(ok), ok).toBe(true);
    for (const bad of ["10.20.0.0", "10.0.0.0/33", "256.0.0.0/8", "example.com/8", "fd00::/129", "1::2::3/64", "10.0.0.0/ 8"]) expect(isCidr(bad), bad).toBe(false);
    expect(networksProblem(["10.0.0.0/8"])).toBeUndefined();
    expect(networksProblem(["a", "b"])).toMatch(/These aren't networks: a, b/);
  });
});

describe("choosing a team's own model", () => {
  it("groups the team's own models apart from the platform's in an agent's model select", async () => {
    mockApi({
      ...shellRoutes("none", "editor"),
      [`GET /v1/agents/${agentId}`]: () => agent(),
      [`GET /v1/teams/${team.slug}/models`]: () => [model(), teamModel],
    });
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=settings`);
    const select = await screen.findByRole("combobox", { name: "Model" });
    await waitFor(() => expect(select.querySelector('optgroup[label="This team"]')).not.toBeNull());
    expect(select.querySelector('optgroup[label="This team"]')).toHaveTextContent("Own One · Team vLLM");
    expect(select.querySelector('optgroup[label="Platform"]')).toHaveTextContent("Fake One · LiteLLM gateway");
  });

  it("offers a platform template only the platform's models", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...adminRoutes(),
      "GET /v1/admin/agent-templates": () => [],
      "GET /v1/admin/mcp-servers": () => [],
      "GET /v1/admin/audit": () => page([]),
    });
    renderApp("/admin/templates");
    await u.click(await screen.findByRole("button", { name: "New platform template" }));
    const dialog = await screen.findByRole("dialog", { name: "New platform template" });
    const select = within(dialog).getByRole("combobox", { name: "Model" });
    await waitFor(() => expect(within(select).getByRole("option", { name: "Fake One · LiteLLM gateway" })).toBeInTheDocument());
    expect(within(select).queryByRole("option", { name: /Own One/ })).toBeNull();
    expect(calls.some((c) => c.url === "/v1/admin/models" && c.search.get("owner") === "platform")).toBe(true);
  });

  it("says why a template on a team's own model can't go to the catalog (409 team_model)", async () => {
    const u = userEvent.setup();
    const templateId = "24242424-2424-4242-8242-242424242424";
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
      currentVersion: 1,
      revision: 1,
      createdAt: "2026-10-01T10:00:00Z",
      updatedAt: "2026-10-01T10:00:00Z",
    };
    mockApi({
      ...shellRoutes("none", "owner"),
      [`GET /v1/agent-templates/${templateId}`]: () => template,
      [`GET /v1/agent-templates/${templateId}/versions`]: () => [],
      [`POST /v1/agent-templates/${templateId}/publish`]: () => Reply.error(409, "team_model", "That model is of a team's own provider, which only that team can use"),
    });
    renderApp(`/templates/${templateId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Helper" })).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "More actions" }));
    await u.click(await screen.findByRole("menuitem", { name: "Submit to the catalog" }));
    // The toast (and its announcement for screen readers).
    expect((await screen.findAllByText("Couldn't submit it to the catalog"))[0]).toBeInTheDocument();
    expect(screen.getAllByText(/runs on one of the team's own models, which only the team can use/)[0]).toBeInTheDocument();
  });
});
