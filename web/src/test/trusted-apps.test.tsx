/* Admin › Settings › Trusted apps: the list, its client-ID checks, the save with If-Match and confirmation, a refused entry, a conflict, and auditors reading. */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { clientIdProblem } from "../pages/admin/settings/trusted-apps";
import { trustedClients } from "./fixtures-oauth";
import { type Call, type Handler, Reply, mockApi, renderApp, shellRoutes } from "./harness";

type Clients = Schemas["TrustedClients"];
const path = "/v1/admin/settings/oauth-clients";
const puts = (calls: Call[]) => calls.filter((c) => c.method === "PUT" && c.url === path);

const personal: Schemas["PersonalWorkspaceSettings"] = { enabled: true, maxAgents: 5, maxRunning: 2, monthlyBudgetUsd: null, revision: 7, updatedAt: "2026-10-18T10:00:00Z" };

const routes = (role: "platform_admin" | "platform_auditor" = "platform_admin", extra: Record<string, Handler> = {}): Record<string, Handler> => ({
  ...shellRoutes(role, null),
  "GET /v1/admin/settings/personal-workspaces": () => personal,
  [`GET ${path}`]: () => trustedClients(),
  ...extra,
});

/** The section, once its list has loaded (the loading section is another element). */
const section = async () => (await screen.findByRole("table", { name: "Trusted apps" })).closest("section")!;

describe("Admin › Settings › Trusted apps", () => {
  it("explains what trusting does, and adds an app after confirming (If-Match)", async () => {
    const u = userEvent.setup();
    const calls = mockApi(routes("platform_admin", { [`PUT ${path}`]: (body) => trustedClients({ clients: (body as Clients).clients, revision: 3 }) }));
    const { container } = renderApp("/admin/settings");
    const card = await section();
    expect(card).toHaveTextContent("skip the approval screen");
    expect(card).toHaveTextContent("People still sign in");
    const table = await within(card).findByRole("table", { name: "Trusted apps" });
    expect(within(table).getByText("https://chat.example.com/oauth/client-metadata.json")).toBeInTheDocument();
    expect(within(table).getByText("Vouched for by notes.example.org")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();

    await u.click(within(card).getByRole("button", { name: "Add app" }));
    const dialog = await screen.findByRole("dialog", { name: "Add trusted app" });
    const id = within(dialog).getByRole("textbox", { name: "Client ID" });
    // A registered app's ID can't be trusted.
    await u.type(id, "pdpcl_3f9a1c");
    await u.click(within(dialog).getByRole("button", { name: "Add app" }));
    expect(within(dialog).getByText(/Apps that registered themselves can't be trusted/)).toBeInTheDocument();
    await u.clear(id);
    await u.type(id, "https://chat.example.com/oauth/client-metadata.json");
    expect(within(dialog).getByText("This app is already on the list.")).toBeInTheDocument();
    await u.clear(id);
    await u.type(id, "https://desk.example.com/oauth/client.json");
    await u.type(within(dialog).getByRole("textbox", { name: /Label/ }), "Desk");
    await u.click(within(dialog).getByRole("button", { name: "Add app" }));
    expect(await within(table).findByText("https://desk.example.com/oauth/client.json")).toBeInTheDocument();
    expect(puts(calls)).toHaveLength(0);

    await u.click(screen.getByRole("button", { name: "Save trusted apps" }));
    const ask = await screen.findByRole("alertdialog", { name: "Save the trusted apps?" });
    expect(ask).toHaveTextContent("Trusted from now on: Desk.");
    await u.click(within(ask).getByRole("button", { name: "Save trusted apps" }));
    await waitFor(() => expect(puts(calls)).toHaveLength(1));
    expect(puts(calls)[0]?.headers.get("If-Match")).toBe('"2"');
    expect((puts(calls)[0]?.body as Clients).clients).toEqual([
      { clientId: "https://chat.example.com/oauth/client-metadata.json", label: "Team Chat" },
      { clientId: "https://notes.example.org/oauth/client.json", label: "Notes" },
      { clientId: "https://desk.example.com/oauth/client.json", label: "Desk" },
    ]);
    expect(await screen.findByText("Trusted apps saved")).toBeInTheDocument();
    // The personal workspace settings weren't touched.
    expect(calls.some((c) => c.method === "PUT" && c.url === "/v1/admin/settings/personal-workspaces")).toBe(false);
  });

  it("names removed apps in the confirmation, and shows a refused app on its row", async () => {
    const u = userEvent.setup();
    mockApi(routes("platform_admin", { [`PUT ${path}`]: () => Reply.error(422, "invalid_client_id", "entry 0", { index: 0, reason: "not_metadata_url" }) }));
    renderApp("/admin/settings");
    const table = await within(await section()).findByRole("table", { name: "Trusted apps" });
    await u.click(within(table).getByRole("button", { name: "Actions for Notes" }));
    await u.click(await screen.findByRole("menuitem", { name: "Remove" }));
    expect(within(table).queryByText("Notes")).toBeNull();
    await u.click(screen.getByRole("button", { name: "Save trusted apps" }));
    const ask = await screen.findByRole("alertdialog");
    expect(ask).toHaveTextContent("Asks for approval again, and its app secrets are deleted (trusting it again needs new ones): Notes.");
    await u.click(within(ask).getByRole("button", { name: "Save trusted apps" }));
    expect(await within(table).findByText(/The platform doesn't accept this ID/)).toBeInTheDocument();
  });

  it("keeps the edits when the list changed elsewhere (412)", async () => {
    const u = userEvent.setup();
    let revision = 2;
    mockApi(
      routes("platform_admin", {
        [`GET ${path}`]: () => trustedClients({ revision }),
        [`PUT ${path}`]: () => {
          revision = 4;
          return Reply.error(412, "revision_conflict", "stale");
        },
      }),
    );
    const { qc } = renderApp("/admin/settings");
    const table = await within(await section()).findByRole("table", { name: "Trusted apps" });
    await u.click(within(table).getByRole("button", { name: "Actions for Notes" }));
    await u.click(await screen.findByRole("menuitem", { name: "Remove" }));
    await u.click(screen.getByRole("button", { name: "Save trusted apps" }));
    await u.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Save trusted apps" }));
    expect(await screen.findByText("Someone else changed these settings while you were editing")).toBeInTheDocument();
    // The app reloads what's on screen after a 412 (main.tsx); the edits stay, on the latest revision.
    await qc.invalidateQueries();
    expect(await screen.findByRole("button", { name: "Overwrite with mine" })).toBeInTheDocument();
    expect(within(table).queryByText("Notes")).toBeNull();
  });

  it("is read-only for auditors", async () => {
    mockApi(routes("platform_auditor"));
    renderApp("/admin/settings");
    const card = await section();
    expect(await within(card).findByText("https://chat.example.com/oauth/client-metadata.json")).toBeInTheDocument();
    expect(screen.getByText("You can view these settings. Only platform admins can change them.")).toBeInTheDocument();
    expect(within(card).queryByRole("button", { name: "Add app" })).toBeNull();
    expect(within(card).queryByRole("button", { name: /Actions for/ })).toBeNull();
  });
});

describe("trusted app client IDs", () => {
  it("accepts only a metadata document's https address with a path", () => {
    expect(clientIdProblem("https://chat.example.com/oauth/client-metadata.json")).toBeUndefined();
    expect(clientIdProblem("")).toMatch(/Enter the app's client ID/);
    expect(clientIdProblem("pdpcl_123")).toMatch(/can't be trusted/);
    expect(clientIdProblem("http://chat.example.com/client.json")).toBe("Only https addresses can be trusted.");
    expect(clientIdProblem("https://chat.example.com/")).toMatch(/Add the document's path/);
    expect(clientIdProblem("https://chat.example.com/c.json#x")).toMatch(/fragment/);
    expect(clientIdProblem("https://u:p@chat.example.com/c.json")).toMatch(/user name and password/);
  });
});
