/* Admin › Models (add a provider, discover its models) and an agent's model (Overview warning, Settings select). */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Schemas } from "../api/client";
import { agent, agentId, model, modelProvider, providerId, providerKinds, team } from "./fixtures";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

const made = (calls: Call[], method: string, url: string) => calls.filter((c) => c.method === method && c.url === url);
const providerPath = `/v1/admin/model-providers/${providerId}`;

const adminRoutes = (role: Schemas["PlatformRole"] = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/model-provider-kinds": () => providerKinds,
  "GET /v1/admin/model-providers": () => [modelProvider()],
  "GET /v1/admin/models": () => [model()],
  [`GET ${providerPath}`]: () => modelProvider(),
  [`GET ${providerPath}/models`]: () => [model()],
  ...extra,
});

describe("Admin › Models › Add provider", () => {
  it("prefills the type's base URL, requires one for compatible servers, and sends the provider", async () => {
    const u = userEvent.setup();
    const created = modelProvider({ id: "new-provider", name: "Local vLLM", accessMode: "direct", modelCount: 0 });
    const calls = mockApi(
      adminRoutes("platform_admin", {
        "POST /v1/admin/model-providers": () => new Reply(201, { data: created }),
        "GET /v1/admin/model-providers/new-provider": () => created,
        "GET /v1/admin/model-providers/new-provider/models": () => [],
      }),
    );
    const { router } = renderApp("/admin/models");
    expect(await screen.findByRole("heading", { level: 1, name: "Models" })).toBeInTheDocument();
    expect(await within(screen.getByRole("table", { name: "Providers" })).findByText("LiteLLM gateway")).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "Add provider" }));
    const dialog = await screen.findByRole("dialog", { name: "Add provider" });
    const type = within(dialog).getByRole("combobox", { name: "Type" });
    const url = within(dialog).getByRole("textbox", { name: "Base URL" });
    await waitFor(() => expect(type).toHaveValue("openai"));
    expect(url).toHaveValue("https://api.openai.com/v1");
    await u.selectOptions(type, "anthropic");
    expect(url).toHaveValue("https://api.anthropic.com");
    await u.selectOptions(type, "openai-compatible");
    expect(url).toHaveValue("");

    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "Local vLLM");
    await u.type(within(dialog).getByLabelText("API key"), "sk-secret");
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    expect(await within(dialog).findByText("Enter the server's address.")).toBeInTheDocument();
    expect(made(calls, "POST", "/v1/admin/model-providers")).toHaveLength(0);

    await u.type(url, "http://vllm.internal:8000/v1");
    await u.click(within(dialog).getByRole("radio", { name: /Direct/ }));
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(made(calls, "POST", "/v1/admin/model-providers")).toHaveLength(1));
    expect(made(calls, "POST", "/v1/admin/model-providers")[0]!.body).toEqual({
      name: "Local vLLM",
      kind: "openai-compatible",
      baseUrl: "http://vllm.internal:8000/v1",
      apiKey: "sk-secret",
      accessMode: "direct",
    });
    await waitFor(() => expect(router.state.location.pathname).toBe("/admin/models/providers/new-provider"));
    // The key is never shown again.
    expect(document.body.textContent).not.toContain("sk-secret");
  });

  it("sends the default base URL and proxy mode unless changed", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes("platform_admin", { "POST /v1/admin/model-providers": () => new Reply(201, { data: modelProvider() }) }));
    renderApp("/admin/models");
    await u.click(await screen.findByRole("button", { name: "Add provider" }));
    const dialog = await screen.findByRole("dialog", { name: "Add provider" });
    await waitFor(() => expect(within(dialog).getByRole("textbox", { name: "Base URL" })).toHaveValue("https://api.openai.com/v1"));
    expect(within(dialog).getByRole("radio", { name: /Through the platform/ })).toBeChecked();
    await u.type(within(dialog).getByRole("textbox", { name: "Name" }), "OpenAI");
    await u.type(within(dialog).getByLabelText("API key"), "sk-1");
    await u.click(within(dialog).getByRole("button", { name: "Add provider" }));
    await waitFor(() => expect(made(calls, "POST", "/v1/admin/model-providers")).toHaveLength(1));
    expect(made(calls, "POST", "/v1/admin/model-providers")[0]!.body).toMatchObject({ kind: "openai", baseUrl: "https://api.openai.com/v1", apiKey: "sk-1", accessMode: "proxy" });
  });

  it("lets auditors read the providers, without adding one", async () => {
    mockApi(adminRoutes("platform_auditor"));
    renderApp("/admin/models");
    expect(await within(await screen.findByRole("table", { name: "Providers" })).findByText("LiteLLM gateway")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add provider" })).toBeNull();
  });
});

describe("Admin › Models › Discover models", () => {
  it("lists the provider's models, with those already added marked and disabled, and adds the picked ones", async () => {
    const u = userEvent.setup();
    const discovered: Schemas["DiscoveredModel"][] = [
      { id: "fake-one", name: "Fake One", contextWindow: 128000, added: true },
      { id: "fake-two", name: "Fake Two", contextWindow: 200000, added: false },
    ];
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`POST ${providerPath}/discover`]: () => discovered,
        [`POST ${providerPath}/models`]: (body) => new Reply(201, { data: model({ id: "m2", upstreamId: "fake-two", name: "Fake Two", ...(body as object) }) }),
      }),
    );
    renderApp(`/admin/models/providers/${providerId}`);
    expect(await screen.findByRole("heading", { level: 1, name: "LiteLLM gateway" })).toBeInTheDocument();
    await u.click(screen.getAllByRole("button", { name: "Discover models" })[0]!);
    const dialog = await screen.findByRole("dialog", { name: "Discover models from LiteLLM gateway" });
    const added = await within(dialog).findByRole("checkbox", { name: "Fake One" });
    expect(added).toBeChecked();
    expect(added).toHaveAttribute("aria-disabled", "true");
    expect(within(dialog).getByText(/Already added/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Add 0 models" })).toBeDisabled();
    await u.click(within(dialog).getByRole("checkbox", { name: "Fake Two" }));
    await u.click(within(dialog).getByRole("button", { name: "Add 1 model" }));
    await waitFor(() => expect(made(calls, "POST", `${providerPath}/models`)).toHaveLength(1));
    expect(made(calls, "POST", `${providerPath}/models`)[0]!.body).toEqual({ upstreamId: "fake-two", name: "Fake Two", contextWindow: 200000 });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: /Discover models/ })).toBeNull());
  });

  it("shows why discovery failed", async () => {
    const u = userEvent.setup();
    mockApi(adminRoutes("platform_admin", { [`POST ${providerPath}/discover`]: () => Reply.error(409, "discovery_failed", "The provider answered 401: invalid API key") }));
    renderApp(`/admin/models/providers/${providerId}`);
    await u.click((await screen.findAllByRole("button", { name: "Discover models" }))[0]!);
    const dialog = await screen.findByRole("dialog", { name: "Discover models from LiteLLM gateway" });
    expect(await within(dialog).findByText("The provider answered 401: invalid API key")).toBeInTheDocument();
  });
});

describe("an agent's model", () => {
  const base = `/teams/${team.slug}/agents/${agentId}`;
  const agentRoutes = (a: Schemas["Agent"], extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes("none", "editor"),
    [`GET /v1/agents/${agentId}`]: () => a,
    [`GET /v1/teams/${team.slug}/models`]: () => [model(), model({ id: "m2", name: "Fake Two" })],
    ...extra,
  });

  it("warns when the model isn't available, with a way to choose another", async () => {
    mockApi(agentRoutes(agent({ model: { id: "gone", name: "Old Model", providerName: "LiteLLM gateway", source: "template", available: false } })));
    renderApp(base);
    expect(await screen.findByText("Its model isn't available")).toBeInTheDocument();
    expect(screen.getByText(/The agent won't start until it's moved to another model/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Choose another model" })).toHaveAttribute("href", `${base}?tab=settings`);
    expect(screen.getByText("From the template")).toBeInTheDocument();
  });

  it("sends \"\" for the template's model, with If-Match", async () => {
    const u = userEvent.setup();
    const own = agent({ model: { id: "m2", name: "Fake Two", providerName: "LiteLLM gateway", source: "agent", available: true } });
    const calls = mockApi(agentRoutes(own, { [`PATCH /v1/agents/${agentId}`]: () => agent({ revision: 5 }) }));
    renderApp(`${base}?tab=settings`);
    const select = await screen.findByRole("combobox", { name: "Model" });
    await waitFor(() => expect(select).toHaveValue("m2"));
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["Template's model", "Fake One · LiteLLM gateway", "Fake Two · LiteLLM gateway"]);
    await u.selectOptions(select, "");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", `/v1/agents/${agentId}`)).toHaveLength(1));
    const patch = made(calls, "PATCH", `/v1/agents/${agentId}`)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"4"');
    expect(patch.body).toMatchObject({ modelId: "" });
  });

  it("leaves the model out when only the name changed", async () => {
    const u = userEvent.setup();
    const calls = mockApi(agentRoutes(agent(), { [`PATCH /v1/agents/${agentId}`]: () => agent({ name: "Helper 2", revision: 5 }) }));
    renderApp(`${base}?tab=settings`);
    const name = await screen.findByRole("textbox", { name: "Name" });
    await u.type(name, " 2");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", `/v1/agents/${agentId}`)).toHaveLength(1));
    expect(made(calls, "PATCH", `/v1/agents/${agentId}`)[0]!.body).not.toHaveProperty("modelId");
  });
});

describe("a template version's model", () => {
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
    { version: 2, instructions: "Be brief.", notes: "Shorter", modelId: model().id, modelName: "Fake One", modelProviderName: "LiteLLM gateway", createdAt: "2026-10-02T10:00:00Z" },
    { version: 1, instructions: "Be helpful.", notes: "Initial version", createdAt: "2026-10-01T10:00:00Z" },
  ];

  it("shows each version's model, and a new version keeps the current one unless changed", async () => {
    const u = userEvent.setup();
    const calls = mockApi({
      ...shellRoutes("none", "editor"),
      [`GET /v1/agent-templates/${templateId}`]: () => template,
      [`GET /v1/agent-templates/${templateId}/versions`]: () => versions,
      [`GET /v1/teams/${team.slug}/models`]: () => [model(), model({ id: "m2", name: "Fake Two" })],
      [`POST /v1/agent-templates/${templateId}/versions`]: () => new Reply(201, { data: { ...template, currentVersion: 3 } }),
    });
    renderApp(`/templates/${templateId}?tab=versions`);
    expect(await screen.findByText("Fake One · LiteLLM gateway")).toBeInTheDocument();
    expect(screen.getByText("Platform default")).toBeInTheDocument();
    await u.click(screen.getByRole("button", { name: "New version" }));
    const dialog = await screen.findByRole("dialog", { name: "New version of Helper" });
    const select = within(dialog).getByRole("combobox", { name: "Model" });
    await waitFor(() => expect(select).toHaveValue(model().id));
    await u.click(within(dialog).getByRole("button", { name: "Save v3" }));
    await waitFor(() => expect(made(calls, "POST", `/v1/agent-templates/${templateId}/versions`)).toHaveLength(1));
    expect(made(calls, "POST", `/v1/agent-templates/${templateId}/versions`)[0]!.body).not.toHaveProperty("modelId");
  });
});

describe("Admin › Models › a provider's settings and models", () => {
  it("sends a new API key only when one is typed, with If-Match", async () => {
    const u = userEvent.setup();
    const calls = mockApi(adminRoutes("platform_admin", { [`PATCH ${providerPath}`]: () => modelProvider({ name: "Gateway", revision: 3 }) }));
    renderApp(`/admin/models/providers/${providerId}?tab=settings`);
    const name = await screen.findByRole("textbox", { name: "Name" });
    await u.clear(name);
    await u.type(name, "Gateway");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", providerPath)).toHaveLength(1));
    const first = made(calls, "PATCH", providerPath)[0]!;
    expect(first.headers.get("If-Match")).toBe('"2"');
    expect(first.body).toEqual({ name: "Gateway", baseUrl: "https://litellm.example.com/v1", accessMode: "proxy", enabled: true });
    await u.type(screen.getByLabelText("Replace API key"), "sk-new");
    await u.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(made(calls, "PATCH", providerPath)).toHaveLength(2));
    expect(made(calls, "PATCH", providerPath)[1]!.body).toMatchObject({ apiKey: "sk-new" });
  });

  it("is read-only for auditors", async () => {
    mockApi(adminRoutes("platform_auditor"));
    renderApp(`/admin/models/providers/${providerId}?tab=settings`);
    expect(await screen.findByRole("textbox", { name: "Name" })).toBeDisabled();
    expect(screen.queryByLabelText("Replace API key")).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete provider" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Discover models" })).toBeNull();
  });

  it("edits a model on a form page: publishes it to chosen teams and clears a price", async () => {
    const u = userEvent.setup();
    const m = model({ inputPrice: 3 });
    const calls = mockApi(
      adminRoutes("platform_admin", {
        [`GET ${providerPath}/models`]: () => [m],
        "GET /v1/admin/teams": () => ({ items: [{ team, memberCount: 4, ownerCount: 1, ownerInvites: 0 }], nextCursor: null }),
        [`PATCH /v1/admin/models/${m.id}`]: (body) => model({ ...(body as object), revision: 2 }),
      }),
    );
    renderApp(`/admin/models/providers/${providerId}?form=${m.id}`);
    expect(await screen.findByRole("heading", { level: 1, name: "Edit Fake One" })).toBeInTheDocument();
    await u.clear(screen.getByRole("textbox", { name: /Input price/ }));
    await u.click(screen.getByRole("button", { name: "Save model" }));
    await waitFor(() => expect(made(calls, "PATCH", `/v1/admin/models/${m.id}`)).toHaveLength(1));
    const patch = made(calls, "PATCH", `/v1/admin/models/${m.id}`)[0]!;
    expect(patch.headers.get("If-Match")).toBe('"1"');
    expect(patch.body).toMatchObject({ name: "Fake One", contextWindow: 128000, maxTokens: 16000, inputPrice: -1, visibility: "all" });
    expect(patch.body).not.toHaveProperty("outputPrice");
  });

  it("surfaces model_in_use when deleting a provider", async () => {
    const u = userEvent.setup();
    mockApi(adminRoutes("platform_admin", { [`DELETE ${providerPath}`]: () => Reply.error(409, "model_in_use", "2 agents use its models") }));
    renderApp(`/admin/models/providers/${providerId}?tab=settings`);
    await u.click(await screen.findByRole("button", { name: "Delete provider" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete LiteLLM gateway?" });
    await u.click(within(ask).getByRole("button", { name: "Delete provider" }));
    expect(await within(ask).findByText("2 agents use its models")).toBeInTheDocument();
  });
});
