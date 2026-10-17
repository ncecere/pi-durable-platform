/* The role badge explains what a team role can and can't do, on the Members list, the overview and Team settings; with axe. */
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { axe } from "vitest-axe";
import { RoleBadge } from "../components/role-badge";
import { canManage, roleAbilities, teamRoles } from "../components/roles";
import { renderBare } from "./harness";

describe("the role badge", () => {
  it("opens what an editor can and can't do", async () => {
    const { container } = renderBare(<RoleBadge role="editor" prefix="Your role: " tone="info" />);
    const badge = await screen.findByRole("button", { name: "Your role: Editor: what this role can do" });
    expect(badge).toHaveTextContent("Your role: Editor");
    await userEvent.click(badge);
    const dialog = await screen.findByRole("dialog", { name: "What an editor can do" });
    expect(within(dialog).getByText("Read the team's audit log")).toBeInTheDocument();
    // Owners and admins manage service accounts, not editors.
    expect(within(dialog).getByText("Manage service accounts")).toBeInTheDocument();
    expect(within(dialog).getByText("Manage members or invites")).toBeInTheDocument();
    expect(await axe(container)).toHaveNoViolations();
  });

  it("has something to say about every role", () => {
    for (const r of teamRoles) {
      expect(roleAbilities[r].can.length).toBeGreaterThan(0);
      expect(roleAbilities[r].cannot.length).toBeGreaterThan(0);
    }
  });
});

describe("canManage", () => {
  it("mirrors the server: owners manage everyone, admins manage non-owners, others nobody", () => {
    expect(canManage("owner", "owner", "member")).toBe(true);
    expect(canManage("admin", "editor", "member")).toBe(true);
    expect(canManage("admin", "owner")).toBe(false);
    expect(canManage("admin", "member", "owner")).toBe(false);
    expect(canManage("editor", "member")).toBe(false);
    expect(canManage(undefined, "member")).toBe(false);
  });
});
