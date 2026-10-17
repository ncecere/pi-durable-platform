/* Home (your teams, the admin card for platform staff) and Profile. */
import { screen, within } from "@testing-library/react";
import { axe } from "vitest-axe";
import { meFor, myTeam, team } from "./fixtures";
import { mockApi, renderApp, shellRoutes } from "./harness";

describe("home", () => {
  it("lists your teams with your role and quick links", async () => {
    mockApi({ ...shellRoutes(), "GET /v1/me": () => ({ ...meFor(), teams: [myTeam("owner"), myTeam("member", { id: "t2", slug: "data", name: "Data Team", status: "archived" })] }) });
    const { container } = renderApp("/");
    const teams = await screen.findByRole("list", { name: "Your teams" });
    const rows = within(teams).getAllByRole("listitem");
    expect(rows[0]).toHaveTextContent("Platform ToolsOwner");
    expect(rows[1]).toHaveTextContent("Data TeamMemberArchived");
    expect(within(rows[0]!).getByRole("link", { name: "Platform Tools: team settings" })).toHaveAttribute("href", `/teams/${team.slug}/settings`);
    expect(screen.queryByText("Platform administration")).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("gives platform staff the way into Admin", async () => {
    mockApi(shellRoutes("platform_auditor"));
    renderApp("/");
    expect(await screen.findByText("Platform administration")).toBeInTheDocument();
    expect(screen.getByText("See users, teams and the audit log, read-only.")).toBeInTheDocument();
    const card = screen.getByText("Platform administration").closest("section")!;
    expect(within(card).getByRole("link", { name: "Audit log" })).toHaveAttribute("href", "/admin/audit");
    expect(screen.getByRole("link", { name: /Open Admin/ })).toHaveAttribute("href", "/admin");
  });
});

describe("home without a team", () => {
  it("explains teams and points at Agents", async () => {
    mockApi(shellRoutes("none", null));
    const { container } = renderApp("/");
    expect(await screen.findByText("You aren't on a team yet.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open Agents/ })).toHaveAttribute("href", "/agents");
    expect(await axe(container)).toHaveNoViolations();
  });
});

const agentBase = {
  description: "A general helper.",
  templateSlug: "assistant",
  templateName: "Assistant",
  enabled: true,
  revision: 1,
  createdAt: "2026-10-17T12:00:00Z",
  updatedAt: "2026-10-17T12:00:00Z",
};
const assistant = { ...agentBase, id: "a1", kind: "personal", name: "My assistant", running: false };
const teamAgent = { ...agentBase, id: "a2", kind: "team", name: "Triage", running: true, teamId: team.id, teamSlug: team.slug, teamName: team.name };

describe("agents", () => {
  it("lists the person's assistant and their teams' agents, in the sidebar for everyone", async () => {
    mockApi({ ...shellRoutes("none", "member"), "GET /v1/agents": () => [assistant, teamAgent] });
    const { container } = renderApp("/agents");
    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "My assistant", current: false })).toHaveAttribute("href", "/assistant");
    expect(screen.getByRole("link", { name: "Triage" })).toHaveAttribute("href", "/agents/a2");
    expect(screen.getByText("Hibernating")).toBeInTheDocument();
    expect(screen.getByText("Awake")).toBeInTheDocument();
    // Plain members can't create team agents.
    expect(screen.queryByRole("button", { name: /New team agent/ })).not.toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Agents" })).toHaveAttribute("aria-current", "page");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("opens the person's assistant, creating it on the first visit", async () => {
    const calls = mockApi({ ...shellRoutes("none", "member"), "POST /v1/me/assistant": () => assistant, "GET /v1/agents/a1/threads": () => [] });
    renderApp("/assistant");
    expect(await screen.findByText("Hi, I'm My assistant.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "What time is it?" })).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url === "/v1/me/assistant")).toBe(true);
  });
});

describe("profile", () => {
  it("shows who you are, your platform role and your teams", async () => {
    mockApi(shellRoutes("platform_admin"));
    const { container } = renderApp("/settings/profile");
    expect(await screen.findByRole("heading", { level: 1, name: "Profile" })).toBeInTheDocument();
    const you = screen.getByRole("heading", { name: "You" }).closest("section")!;
    expect(within(you).getByText("una@example.com")).toBeInTheDocument();
    expect(within(you).getByText("Platform admin")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: team.name })).toHaveAttribute("href", `/teams/${team.slug}`);
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Profile");
    const groups = screen.getByRole("heading", { name: "Your groups" }).closest("section")!;
    expect(groups).toHaveTextContent("From your identity provider");
    expect(within(within(groups).getByRole("list", { name: "Your groups" })).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["everyone", "engineering"]);
    expect(within(groups).queryByRole("button")).toBeNull();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("says when the identity provider sent no groups", async () => {
    mockApi({ ...shellRoutes("none"), "GET /v1/me": () => ({ ...meFor("none"), groups: [] }) });
    renderApp("/settings/profile");
    expect(await screen.findByText("Your identity provider didn't send any groups.")).toBeInTheDocument();
  });

  it("explains how to join a team when you have none", async () => {
    mockApi(shellRoutes("none", null));
    renderApp("/settings/profile");
    expect(await screen.findByText(/You aren't on a team yet\. To join a team, ask one of its owners/)).toBeInTheDocument();
  });
});
