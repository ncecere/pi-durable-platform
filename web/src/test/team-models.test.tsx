/* Team › Models: the team's own providers and the models it may use, adding a provider, and a team provider's page (no access mode, no publishing). */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { meFor, model, modelProvider, myTeam, providerId, providerKinds, team } from "./fixtures";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const teamPath = `/v1/teams/${team.slug}`;
const owner: Schemas["ModelOwner"] = { id: team.id, slug: team.slug, name: team.name, kind: "team" };
const teamProvider = (extra: Partial<Schemas["ModelProvider"]> = {}) => modelProvider({ name: "Team vLLM", baseUrl: "https://vllm.example.com/v1", team: owner, ...extra });
const teamModel = (extra: Partial<Schemas["Model"]> = {}) =>
  model({ id: "own-1", name: "Own One", upstreamId: "own-one", providerName: "Team vLLM", visibility: "teams", personal: false, providerTeam: owner, ...extra });
const platformModel = () => model({ id: "plat-1", name: "Platform One", providerId: "platform-provider", providerName: "OpenAI" });

const limits = (canAddProviders: boolean, kind: "team" | "personal" = "team"): Schemas["WorkspaceLimits"] => ({
  kind,
  overridden: [],
  agentCount: 0,
  runningCount: 0,
  canAddProviders,
});

const teamRoutes = (role: Schemas["TeamRole"] = "owner", canAdd = true, extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes("none", role),
  [`GET ${teamPath}/model-provider-kinds`]: () => providerKinds,
  [`GET ${teamPath}/model-providers`]: () => [teamProvider()],
  [`GET ${teamPath}/models`]: () => [teamModel(), platformModel()],
  [`GET ${teamPath}/limits`]: () => limits(canAdd),
  [`GET ${teamPath}/model-providers/${providerId}`]: () => teamProvider(),
  [`GET ${teamPath}/model-providers/${providerId}/models`]: () => [teamModel()],
  ...extra,
});

describe("Team › Models", () => {
  it("shows the team's providers (no Access column) and, on Models, every model it may use with its source", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes());
    const { router, container } = renderApp(`/teams/${team.slug}/models`);
    expect(await screen.findByRole("heading", { level: 1, name: "Models" })).toBeInTheDocument();
    const providers = await screen.findByRole("table", { name: "Providers" });
    expect(await within(providers).findByText("Team vLLM")).toBeInTheDocument();
    expect(within(providers).queryByRole("columnheader", { name: /Access/ })).toBeNull();
    expect(within(providers).getByRole("link", { name: "Team vLLM" })).toHaveAttribute("href", `/teams/${team.slug}/models/providers/${providerId}`);
    expect(screen.getByRole("button", { name: "Add provider" })).toBeInTheDocument();

    await u.click(within(screen.getByRole("tablist", { name: "Model catalog sections" })).getByRole("tab", { name: /Models/ }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "models" }));
    const table = await screen.findByRole("table", { name: "Models the team may use" });
    const own = (await within(table).findByText("Own One")).closest("tr")!;
    expect(within(own).getByText("This team")).toBeInTheDocument();
    expect(within(own).getByRole("link", { name: "Own One" })).toHaveAttribute("href", `/teams/${team.slug}/models/providers/${providerId}`);
    const platform = within(table).getByText("Platform One").closest("tr")!;
    expect(within(platform).getByText("Platform")).toBeInTheDocument();
    // A platform provider isn't the team's to open.
    expect(within(platform).queryByRole("link", { name: "Platform One" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Add provider" })).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("adds a provider without an access mode, shows a refused address on the URL field, then opens it", async () => {
    const u = userEvent.setup();
    let refuse = true;
    const created = teamProvider({ id: "new-provider", name: "Gateway", modelCount: 0 });
    const calls = mockApi(
      teamRoutes("owner", true, {
        [`POST ${teamPath}/model-providers`]: () =>
          refuse ? Reply.error(422, "address_not_allowed", "The provider's address is on a network teams can't use. Ask a platform admin to allow it.") : new Reply(201, { data: created }),
        [`GET ${teamPath}/model-providers/new-provider`]: () => created,
        [`GET ${teamPath}/model-providers/new-provider/models`]: () => [],
      }),
    );
    const { router } = renderApp(`/teams/${team.slug}/models`);
    await u.click(await screen.findByRole("button", { name: "Add provider" }));
    const dialog = await screen.findByRole("dialog", { name: "Add provider" });
    expect(within(dialog).queryByRole("radio")).toBeNull();
    expect(within(dialog).getByText(/Agents reach it through the platform/)).toBeInTheDocument();
    const type = within(dialog).getByRole("combobox", { name: "Type" });
    await waitFor(() => expect(type).toHaveValue("openai"));
    await u.selectOptions(type, "openai-compatible");
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Gateway");
    const url = within(dialog).getByRole("textbox", { name: "Base URL" });
    await u.type(url, "http://10.1.2.3/v1");
    await u.type(within(dialog).getByLabelText("API key"), "sk-team");
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    // A team's provider needs https.
    expect(await within(dialog).findByText("Enter an address starting with https://.")).toBeInTheDocument();
    expect(made(calls, "POST", `${teamPath}/model-providers`)).toHaveLength(0);

    await u.clear(url);
    await u.type(url, "https://10.1.2.3/v1");
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    expect(await within(dialog).findByText(/on a network teams can't use/)).toBeInTheDocument();
    expect(url).toHaveAttribute("aria-invalid", "true");
    expect(made(calls, "POST", `${teamPath}/model-providers`)[0]!.body).toEqual({ name: "Gateway", kind: "openai-compatible", baseUrl: "https://10.1.2.3/v1", apiKey: "sk-team" });

    refuse = false;
    await u.clear(url);
    await u.type(url, "https://gateway.example.com/v1");
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/teams/${team.slug}/models/providers/new-provider`));
    expect(made(calls, "POST", `${teamPath}/model-providers`)[1]!.body).not.toHaveProperty("accessMode");
  });

  it("says why an owner can't add a provider when platform admins turned it off", async () => {
    mockApi(teamRoutes("owner", false));
    renderApp(`/teams/${team.slug}/models`);
    expect(await screen.findByText("Adding providers is turned off")).toBeInTheDocument();
    expect(screen.getByText(/Platform admins have turned off team model providers/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add provider" })).toBeNull();
  });

  it("says personal workspaces aren't allowed providers, in a personal workspace", async () => {
    const personal: Schemas["Team"] = { ...team, kind: "personal", name: "Personal" };
    mockApi({
      ...teamRoutes("owner", false),
      "GET /v1/me": () => ({ ...meFor(), teams: [myTeam("owner", { kind: "personal", name: "Personal" })] }),
      [`GET ${teamPath}`]: (): Schemas["TeamView"] => ({ team: personal, role: "owner" }),
      [`GET ${teamPath}/limits`]: () => limits(false, "personal"),
    });
    renderApp(`/teams/${team.slug}/models`);
    expect(await screen.findByText(/haven't let personal workspaces add their own model providers/)).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Models" })).toHaveAttribute("href", `/teams/${team.slug}/models`);
  });

  it("is read-only for members: no Add provider, and the provider's page has no actions", async () => {
    mockApi(teamRoutes("member", false));
    renderApp(`/teams/${team.slug}/models`);
    expect(await screen.findByText(/Only its owners and admins can change them/)).toBeInTheDocument();
    expect(screen.queryByText("Adding providers is turned off")).toBeNull();
    expect(screen.queryByRole("button", { name: "Add provider" })).toBeNull();
  });

  it("lets members read a provider's settings without changing them", async () => {
    mockApi(teamRoutes("member", false));
    renderApp(`/teams/${team.slug}/models/providers/${providerId}?tab=settings`);
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Discover models" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete provider" })).toBeNull();
    expect(screen.queryByLabelText("Replace API key")).toBeNull();
  });
});

describe("Team › Models › a provider", () => {
  const providerPath = `${teamPath}/model-providers/${providerId}`;

  it("has no access mode in its settings, saves with If-Match through the team's route, and shows a refused address", async () => {
    const u = userEvent.setup();
    const calls = mockApi(
      teamRoutes("admin", true, { [`PATCH ${providerPath}`]: () => Reply.error(422, "address_unresolved", "The provider's host name could not be resolved") }),
    );
    renderApp(`/teams/${team.slug}/models/providers/${providerId}?tab=settings`);
    const url = await screen.findByRole("textbox", { name: "Base URL" });
    expect(screen.queryByRole("radiogroup", { name: "How agents reach it" })).toBeNull();
    expect(screen.getByText(/Agents reach it through the platform/)).toBeInTheDocument();
    await u.clear(url);
    await u.type(url, "https://nowhere.example/v1");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", providerPath)).toHaveLength(1));
    const patch = made(calls, "PATCH", providerPath)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"2"');
    expect(patch.body).toEqual({ name: "Team vLLM", baseUrl: "https://nowhere.example/v1", enabled: true });
    expect(await screen.findByText("The provider's host name could not be resolved")).toBeInTheDocument();
    expect(url).toHaveAttribute("aria-invalid", "true");
  });

  it("edits a model without publishing controls, and leaves visibility, teams and personal out", async () => {
    const u = userEvent.setup();
    const m = teamModel();
    const calls = mockApi(teamRoutes("owner", true, { [`PATCH ${teamPath}/models/${m.id}`]: (body) => teamModel({ ...(body as object), revision: 2 }) }));
    renderApp(`/teams/${team.slug}/models/providers/${providerId}?form=${m.id}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Edit Own One" })).toBeInTheDocument();
    expect(screen.queryByRole("radiogroup", { name: "Published to" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: /personal workspaces/ })).toBeNull();
    expect(screen.getByText(/Only Platform Tools's templates and agents can use it/)).toBeInTheDocument();
    const name = screen.getByRole("textbox", { name: "Name" });
    await u.clear(name);
    await u.type(name, "Own Model");
    await u.click(screen.getByRole("button", { name: "Save model" }));
    await waitFor(() => expect(made(calls, "PATCH", `${teamPath}/models/${m.id}`)).toHaveLength(1));
    const patch = made(calls, "PATCH", `${teamPath}/models/${m.id}`)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"1"');
    expect(patch.body).toMatchObject({ name: "Own Model", contextWindow: 128000, enabled: true });
    for (const key of ["visibility", "teams", "personal"]) expect(patch.body).not.toHaveProperty(key);
  });

  it("adds a model by hand for the team only", async () => {
    const u = userEvent.setup();
    const calls = mockApi(teamRoutes("owner", true, { [`POST ${providerPath}/models`]: (body) => new Reply(201, { data: teamModel(body as object) }) }));
    renderApp(`/teams/${team.slug}/models/providers/${providerId}?form=new`);
    await u.type(await screen.findByRole("textbox", { name: "Model ID" }), "own-two");
    expect(screen.queryByRole("radiogroup", { name: "Published to" })).toBeNull();
    await u.click(screen.getByRole("button", { name: "Add model" }));
    await waitFor(() => expect(made(calls, "POST", `${providerPath}/models`)).toHaveLength(1));
    const body = made(calls, "POST", `${providerPath}/models`)[0]!.body;
    expect(body).toMatchObject({ upstreamId: "own-two", enabled: true });
    for (const key of ["visibility", "teams", "personal"]) expect(body).not.toHaveProperty(key);
  });

  it("discovers through the team's route, and the breadcrumb names the provider", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes("owner", true, { [`POST ${providerPath}/discover`]: () => [{ id: "own-one", name: "Own One", contextWindow: 32000, added: true }] }));
    renderApp(`/teams/${team.slug}/models/providers/${providerId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Team vLLM" })).toBeInTheDocument();
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    await waitFor(() => expect(within(crumbs).getByRole("link", { name: "Models" })).toHaveAttribute("href", `/teams/${team.slug}/models`));
    await u.click(screen.getAllByRole("button", { name: "Discover models" })[0]!);
    const dialog = await screen.findByRole("dialog", { name: "Discover models from Team vLLM" });
    expect(within(dialog).getByText(/New models are for Platform Tools only/)).toBeInTheDocument();
    expect(await within(dialog).findByRole("checkbox", { name: "Own One" })).toBeChecked();
  });

  it("shows the not-found page for a provider that isn't the team's", async () => {
    mockApi(teamRoutes("owner", true, { [`GET ${teamPath}/model-providers/elsewhere`]: () => Reply.error(404, "not_found", "Not found") }));
    renderApp(`/teams/${team.slug}/models/providers/elsewhere`);
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
  });
});

describe("Team › Models in the command palette", () => {
  it("jumps to the team's models", async () => {
    const u = userEvent.setup();
    mockApi(teamRoutes());
    const { router } = renderApp(`/teams/${team.slug}`);
    await screen.findByRole("heading", { level: 1, name: team.name });
    await u.keyboard("{Control>}k{/Control}");
    const dialog = await screen.findByRole("dialog", { name: "Command palette" });
    await u.type(within(dialog).getByRole("combobox", { name: "Command palette" }), "litellm");
    await u.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe(`/teams/${team.slug}/models`));
  });
});
