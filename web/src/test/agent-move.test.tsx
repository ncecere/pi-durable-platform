/*
 * An agent's Settings › Move to another team: the person picks a team where they are an owner or admin, the dry run
 * (GET …/move) says what changes or what stops the move, and the move is confirmed with the agent's revision.
 */
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import type { Schemas } from "../api/client";
import { agent, agentId, meFor, myTeam, team } from "./fixtures";
import { type Handler, mockApi, renderApp, shellRoutes } from "./harness";

type Plan = Schemas["AgentMovePlan"];

const dest = { id: "77777777-7777-4777-8777-777777777777", slug: "dest", name: "Destination" };
const personal = { id: "88888888-8888-4888-8888-888888888888", slug: "personal-abc", name: "Personal" };

const plan = (to: typeof dest, kind: "team" | "personal", extra: Partial<Plan> = {}): Plan => ({
  agentId,
  from: { id: team.id, slug: team.slug, name: team.name, kind: "team" },
  to: { ...to, kind },
  allowed: true,
  blockers: [],
  sharedThreads: 2,
  privateThreads: 1,
  privateThreadsUnreachable: 1,
  targetMembers: 3,
  targetServiceAccounts: 0,
  peopleLosingAccess: 2,
  serviceAccountsLosingAccess: 1,
  externalKey: null,
  threadMcpServers: 0,
  mcpServersDropped: ["Team wiki"],
  model: null,
  ...extra,
});

function platform(a = agent()) {
  const routes: Record<string, Handler> = {
    ...shellRoutes("none", "owner"),
    "GET /v1/me": () => ({
      ...meFor("none", "owner"),
      teams: [
        myTeam("owner"),
        myTeam("admin", dest),
        myTeam("owner", { ...personal, kind: "personal" }),
        myTeam("member", { id: "99999999-9999-4999-8999-999999999999", slug: "readonly", name: "Read Only" }),
      ],
    }),
    [`GET /v1/agents/${agentId}`]: () => a,
    [`GET /v1/teams/${team.slug}/agents`]: () => [a],
    [`GET /v1/teams/${team.slug}/models`]: () => [],
    [`GET /v1/agents/${agentId}/threads`]: () => [],
    [`GET /v1/agents/${agentId}/move`]: (_, call) =>
      call.search.get("team") === dest.slug
        ? plan(dest, "team")
        : plan(personal, "personal", {
            allowed: false,
            blockers: [{ code: "move_template_unavailable", message: "The agent's template isn't available to the target team" }],
          }),
    [`POST /v1/agents/${agentId}/move`]: () => ({ ...a, teamId: dest.id, teamSlug: dest.slug, teamName: dest.name, revision: a.revision + 1 }),
  };
  return mockApi(routes);
}

describe("Settings › Move to another team", () => {
  it("shows what changes in the chosen team, then moves the agent with its revision", async () => {
    const u = userEvent.setup();
    const calls = platform();
    const { container } = renderApp(`/teams/${team.slug}/agents/${agentId}?tab=settings`);
    await u.click(await screen.findByRole("button", { name: "Move agent…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Helper" });
    const select = within(dialog).getByRole("combobox", { name: "Move to" });
    // Only teams where the person is an owner or admin, not its own.
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual(["Choose a team…", "Destination", "Your personal workspace"]);
    const move = within(dialog).getByRole("button", { name: "Move agent" });
    expect(move).toBeDisabled();

    await u.selectOptions(select, dest.slug);
    expect(await within(dialog).findByText("2 conversations, visible from now on to 3 people in Destination.")).toBeInTheDocument();
    expect(within(dialog).getByText(/1 of them is out of reach/)).toBeInTheDocument();
    expect(within(dialog).getByText("Team wiki stop applying: Destination may not use them.")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();

    await u.click(move);
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.url === `/v1/agents/${agentId}/move`)).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.url === `/v1/agents/${agentId}/move`)!;
    expect(post.body).toEqual({ team: dest.slug });
    expect(post.headers.get("If-Match")).toBe('"4"');
  });

  it("says what stops the move, and doesn't offer it", async () => {
    const u = userEvent.setup();
    const calls = platform();
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=settings`);
    await u.click(await screen.findByRole("button", { name: "Move agent…" }));
    const dialog = await screen.findByRole("dialog", { name: "Move Helper" });
    await u.selectOptions(within(dialog).getByRole("combobox", { name: "Move to" }), personal.slug);
    expect(await within(dialog).findByText("It can't move to the personal workspace yet")).toBeInTheDocument();
    expect(within(dialog).getByText("The agent's template isn't available to the target team")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Move agent" })).toBeDisabled();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("doesn't move a service-managed agent", async () => {
    platform(agent({ externalKey: "user-42", serviceManaged: true }));
    renderApp(`/teams/${team.slug}/agents/${agentId}?tab=settings`);
    expect(await screen.findByRole("button", { name: "Move agent…" })).toBeDisabled();
    expect(screen.getByText(/stays in the app's team/)).toBeInTheDocument();
  });
});
