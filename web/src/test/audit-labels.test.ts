/* Audit words: the initial platform role, humanized roles, and "Not set" in both the list and the record. */
import { auditChange, changedRows, changeSummary, valueText } from "../components/audit/changes";
import { actionLabel, areaOf, targetTypeLabel } from "../components/audit/labels";

describe("audit labels", () => {
  it("names the bootstrap grant neutrally and shows its role in words", () => {
    const e = { action: "platform.bootstrap_role", before: { platformRole: "none" }, after: { platformRole: "platform_admin" } };
    expect(actionLabel(e.action)).toBe("Granted initial platform role");
    expect(changeSummary(e)).toBe("Platform role None → Platform admin");
    expect(changedRows(auditChange(e))).toEqual([{ field: "Platform role", before: "None", after: "Platform admin" }]);
  });

  it("says Not set for a missing value in the list, as the record does", () => {
    const e = { action: "team.update", before: { description: "" }, after: { description: "Tools" } };
    expect(changeSummary(e)).toBe("Description Not set → Tools");
    expect(valueText(undefined)).toBe("Not set");
  });

  it("knows the target types and areas of the kept domains", () => {
    expect(targetTypeLabel("service_account_key")).toBe("Service account key");
    expect(targetTypeLabel("api_key")).toBe("API key");
    expect(targetTypeLabel("some_new_thing")).toBe("some new thing");
    expect(actionLabel("service_account_key.create")).toBe("Created service account key");
    expect(areaOf("service_account.update")).toBe("Service accounts");
    expect(areaOf("api_key.revoke")).toBe("API keys");
    expect(areaOf("platform.user_suspend")).toBe("Platform and users");
    // An action the UI doesn't know yet (a new domain's) is spelled out.
    expect(actionLabel("agent.deploy_started")).toBe("Agent: deploy started");
    const e = { action: "service_account.update", before: { role: "member", enabled: true }, after: { role: "editor", enabled: false } };
    expect(changedRows(auditChange(e))).toEqual([
      { field: "Role", before: "Member", after: "Editor" },
      { field: "Enabled", before: "On", after: "Off" },
    ]);
  });
});
