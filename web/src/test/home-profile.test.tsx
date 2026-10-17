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
  teamId: team.id,
  teamSlug: team.slug,
  teamName: team.name,
  templateId: "t1",
  templateVersion: 1,
  templateCurrentVersion: 2,
};
const triage = { ...agentBase, id: "a1", name: "Triage", running: true };
const perUser = { ...agentBase, id: "a2", name: "Assistant for user 42", running: false, externalKey: "user-42" };

describe("agents", () => {
  it("lists the agents of the person's teams, linking to each team's agent page", async () => {
    mockApi({ ...shellRoutes("none", "owner"), "GET /v1/agents": () => [triage, perUser] });
    const { container } = renderApp("/agents");
    expect(await screen.findByRole("heading", { level: 1, name: "Agents" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Triage" })).toHaveAttribute("href", `/teams/${team.slug}/agents/a1`);
    expect(screen.getByText("user-42")).toBeInTheDocument();
    expect(screen.getByText("Awake")).toBeInTheDocument();
    expect(screen.getByText("Hibernating")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Deploy agent/ })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "All agents" })).toHaveAttribute("aria-current", "page");
    expect(await axe(container)).toHaveNoViolations();
  });

  it("doesn't offer deploying to plain members", async () => {
    mockApi({ ...shellRoutes("none", "member"), "GET /v1/agents": () => [] });
    renderApp("/agents");
    expect(await screen.findByText("No agents yet.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Deploy agent/ })).not.toBeInTheDocument();
  });

  it("opens an agent with its overview, test chat and settings", async () => {
    mockApi({ ...shellRoutes("none", "editor"), "GET /v1/agents/a1": () => triage, "GET /v1/agents/a1/threads": () => [] });
    renderApp(`/teams/${team.slug}/agents/a1?tab=test`);
    expect(await screen.findByRole("heading", { level: 1, name: "Triage" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Overview/ })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Settings/ })).toBeInTheDocument();
    expect(await screen.findByText("Test Triage")).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Breadcrumb" })).toHaveTextContent("Triage");
  });

  it("shows a service-managed agent to the team's owners without a test chat", async () => {
    mockApi({ ...shellRoutes("none", "owner"), "GET /v1/agents/a2": () => perUser });
    renderApp(`/teams/${team.slug}/agents/a2`);
    expect(await screen.findByRole("heading", { level: 1, name: "Assistant for user 42" })).toBeInTheDocument();
    expect(screen.getByText("Service-managed")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /Test/ })).not.toBeInTheDocument();
  });
});

const platformTemplate = {
  id: "t1",
  slug: "assistant",
  name: "Assistant",
  description: "A general helper.",
  status: "published",
  archived: false,
  currentVersion: 2,
  revision: 1,
  createdAt: "2026-10-17T12:00:00Z",
  updatedAt: "2026-10-17T12:00:00Z",
};
const pendingTemplate = { ...platformTemplate, id: "t2", slug: "triage", name: "Triage", status: "pending", teamId: team.id, teamSlug: team.slug, teamName: team.name, agentCount: 0 };

describe("templates", () => {
  it("lists the catalog with each template's owner", async () => {
    mockApi({ ...shellRoutes("none", "editor"), "GET /v1/agent-templates": () => [platformTemplate] });
    const { container } = renderApp("/catalog");
    expect(await screen.findByRole("heading", { level: 1, name: "Catalog" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Assistant" })).toHaveAttribute("href", "/templates/t1");
    expect(screen.getByText("Platform")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /New platform template/ })).not.toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("lets a platform admin review a team's request to publish", async () => {
    mockApi({
      ...shellRoutes("platform_admin", null),
      "GET /v1/agent-templates/t2": () => pendingTemplate,
      "GET /v1/agent-templates/t2/versions": () => [{ version: 2, instructions: "Sort tickets.", notes: "", createdAt: "2026-10-17T12:00:00Z" }],
    });
    renderApp("/templates/t2");
    expect(await screen.findByRole("heading", { level: 1, name: "Triage" })).toBeInTheDocument();
    expect(screen.getByText("Waiting for review")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review request" })).toBeInTheDocument();
    expect(await screen.findByText("Sort tickets.")).toBeInTheDocument();
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
