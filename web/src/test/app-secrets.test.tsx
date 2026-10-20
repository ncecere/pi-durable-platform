/* Admin › Settings › App secrets: the list (never a secret), issuing one shown once, deleting one after confirming, and auditors reading. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { trustedClients } from "./fixtures-oauth";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

const secretsPath = "/v1/admin/settings/oauth-clients/secrets";
const secret = (extra: Partial<Schemas["AppSecret"]> = {}): Schemas["AppSecret"] => ({
  id: "cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd",
  clientId: "https://chat.example.com/oauth/client-metadata.json",
  prefix: "pdpcs_AbCd",
  createdAt: "2026-10-12T10:00:00Z",
  lastUsedAt: null,
  trusted: true,
  ...extra,
});

const routes = (role: "platform_admin" | "platform_auditor", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/settings/personal-workspaces": () => ({ enabled: true, maxAgents: 5, maxRunning: 2, monthlyBudgetUsd: null, revision: 1, updatedAt: "2026-10-18T10:00:00Z" }),
  "GET /v1/admin/settings/oauth-clients": () => trustedClients(),
  [`GET ${secretsPath}`]: () => [secret(), secret({ id: "efefefef-efef-4fef-8fef-efefefefefef", clientId: "https://old.example.com/c.json", prefix: "pdpcs_ZzZz", trusted: false })],
  ...extra,
});

const section = async () => (await screen.findByRole("table", { name: "App secrets" })).closest("section")!;

describe("Admin › Settings › App secrets", () => {
  it("lists secrets by app and prefix, flags an app no longer trusted, and issues a secret shown once", async () => {
    const u = userEvent.setup();
    const issued = "pdpcs_NotesSecretValue1234567890abcdefghijklmnopq";
    const calls = mockApi(
      routes("platform_admin", {
        [`POST ${secretsPath}`]: (body) => ({ ...secret({ clientId: (body as { clientId: string }).clientId, prefix: "pdpcs_Note" }), secret: issued }),
      }),
    );
    const { container } = renderApp("/admin/settings");
    const card = await section();
    expect(card).toHaveTextContent("Team Chat");
    expect(card).toHaveTextContent("pdpcs_AbCd…");
    expect(card).toHaveTextContent("Not trusted: doesn't work");
    expect(await axe(container)).toHaveNoViolations();

    await u.click(within(card).getByRole("button", { name: "Issue secret" }));
    const dialog = await screen.findByRole("dialog", { name: "Issue an app secret" });
    // Team Chat has room for one more; Notes too.
    await u.selectOptions(within(dialog).getByRole("combobox", { name: "App" }), "https://notes.example.org/oauth/client.json");
    await u.click(within(dialog).getByRole("button", { name: "Issue secret" }));
    const shown = await screen.findByRole("dialog", { name: "App secret issued" });
    expect(within(shown).getByText(issued)).toBeInTheDocument();
    expect(shown).toHaveTextContent("It won't be shown again");
    expect(calls.find((c) => c.method === "POST" && c.url === secretsPath)?.body).toEqual({ clientId: "https://notes.example.org/oauth/client.json" });
  });

  it("deletes a secret after confirming", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { [`DELETE ${secretsPath}/cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd`]: () => ({ ok: true }) }));
    renderApp("/admin/settings");
    const card = await section();
    await u.click(within(card).getByRole("button", { name: "Actions for the secret pdpcs_AbCd…" }));
    await u.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    const ask = await screen.findByRole("alertdialog", { name: "Delete this app secret?" });
    expect(ask).toHaveTextContent("Team Chat can no longer use pdpcs_AbCd…");
    await u.click(within(ask).getByRole("button", { name: "Delete secret" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url === `${secretsPath}/cdcdcdcd-cdcd-4dcd-8dcd-cdcdcdcdcdcd`)).toBe(true));
  });

  it("lets auditors read, without issuing or deleting", async () => {
    mockApi(routes("platform_auditor"));
    renderApp("/admin/settings");
    const card = await section();
    expect(card).toHaveTextContent("pdpcs_AbCd…");
    expect(within(card).queryByRole("button", { name: "Issue secret" })).toBeNull();
    expect(within(card).queryByRole("button", { name: /Actions for/ })).toBeNull();
  });
});
