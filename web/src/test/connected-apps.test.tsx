/* Connected apps: the person's own (Account) and anyone's on Admin › Users › person, with Disconnect after confirming; auditors read. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { page, team, user } from "./fixtures";
import { oauthGrant, registeredClient } from "./fixtures-oauth";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

const grants = [oauthGrant(), oauthGrant({ id: "cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd", client: registeredClient(), lastUsedAt: null })];
const firstId = grants[0]!.id;

describe("Account › Connected apps", () => {
  it("lists the person's apps with host, trusted or unverified, connected and last used", async () => {
    mockApi({ ...shellRoutes("none", "member"), "GET /v1/me/oauth-grants": () => grants });
    const { container } = renderApp("/settings/connected-apps");
    expect(await screen.findByRole("heading", { level: 1, name: "Connected apps" })).toBeInTheDocument();
    const table = await screen.findByRole("table", { name: "Your connected apps" });
    const trusted = (await within(table).findByText("Team Chat")).closest("tr")!;
    expect(trusted).toHaveTextContent("chat.example.com");
    expect(within(trusted).getByText("Trusted")).toBeInTheDocument();
    expect(within(trusted).queryByText("Unverified")).toBeNull();
    const unverified = within(table).getByText("Handy Helper").closest("tr")!;
    expect(within(unverified).getByText("Unverified")).toBeInTheDocument();
    expect(unverified).toHaveTextContent("It registered itself");
    expect(unverified).toHaveTextContent("Not used yet");
    await waitFor(() => expect(document.title).toBe("Connected apps · Pi Durable Platform"));
    expect(await axe(container)).toHaveNoViolations();
  });

  it("disconnects an app only after confirming", async () => {
    const u = userEvent.setup();
    const calls = mockApi({ ...shellRoutes("none", "member"), "GET /v1/me/oauth-grants": () => grants, [`DELETE /v1/me/oauth-grants/${firstId}`]: () => ({ ok: true }) });
    renderApp("/settings/connected-apps");
    await u.click(await screen.findByRole("button", { name: "Actions for Team Chat" }));
    await u.click(await screen.findByRole("menuitem", { name: "Disconnect…" }));
    const ask = await screen.findByRole("alertdialog", { name: "Disconnect Team Chat?" });
    expect(ask).toHaveTextContent("It can't act as you any more");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await u.click(within(ask).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url === `/v1/me/oauth-grants/${firstId}`)).toBe(true));
    expect(await screen.findByText("Team Chat was disconnected")).toBeInTheDocument();
    // The list is read again.
    await waitFor(() => expect(calls.filter((c) => c.method === "GET" && c.url === "/v1/me/oauth-grants").length).toBeGreaterThan(1));
  });

  it("explains what connected apps are when there are none", async () => {
    mockApi({ ...shellRoutes("none", null), "GET /v1/me/oauth-grants": () => [] });
    renderApp("/settings/connected-apps");
    expect(await screen.findByText("No connected apps.")).toBeInTheDocument();
    expect(screen.getByText(/Apps you connect with your platform account, such as a chat app, are listed here/)).toBeInTheDocument();
  });

  it("is in the account menu after API keys", async () => {
    const u = userEvent.setup();
    mockApi({ ...shellRoutes("none", "member"), "GET /v1/me/oauth-grants": () => [] });
    renderApp("/settings/profile");
    await u.click(await screen.findByRole("button", { name: /Una User/ }));
    const items = (await screen.findAllByRole("menuitem")).map((i) => i.textContent);
    expect(items.indexOf("Connected apps")).toBe(items.indexOf("API keys") + 1);
  });
});

describe("Admin › Users › person › Connected apps", () => {
  const person = user({ id: "99999999-0000-4000-8000-000000000001", email: "drew@example.com", displayName: "Drew Member" });
  const routes = (role: Schemas["PlatformRole"], extra: Record<string, Handler> = {}): Record<string, Handler> => ({
    ...shellRoutes(role, null),
    [`GET /v1/admin/users/${person.id}`]: (): Schemas["UserDetail"] => ({ user: person, teams: [{ id: team.id, slug: team.slug, name: team.name, status: "active", role: "member" }] }),
    [`GET /v1/admin/users/${person.id}/oauth-grants`]: () => grants,
    "GET /v1/admin/audit": () => page([]),
    ...extra,
  });

  it("lets platform admins disconnect a person's app after confirming", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { [`DELETE /v1/admin/users/${person.id}/oauth-grants/${firstId}`]: () => ({ ok: true }) }));
    const { container } = renderApp(`/admin/users/${person.id}`);
    const table = await screen.findByRole("table", { name: "Apps Drew Member connected" });
    expect(await within(table).findByText("Team Chat")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
    await u.click(within(table).getByRole("button", { name: "Actions for Team Chat" }));
    await u.click(await screen.findByRole("menuitem", { name: "Disconnect…" }));
    const ask = await screen.findByRole("alertdialog", { name: "Disconnect Team Chat?" });
    expect(ask).toHaveTextContent("Drew Member can connect it again");
    await u.click(within(ask).getByRole("button", { name: "Disconnect" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url === `/v1/admin/users/${person.id}/oauth-grants/${firstId}`)).toBe(true));
  });

  it("is read-only for auditors", async () => {
    mockApi(routes("platform_auditor"));
    renderApp(`/admin/users/${person.id}`);
    const table = await screen.findByRole("table", { name: "Apps Drew Member connected" });
    expect(await within(table).findByText("Team Chat")).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: /Actions for/ })).toBeNull();
  });
});
