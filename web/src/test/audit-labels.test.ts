/*
 * Audit words: the initial platform role, humanized roles, and "Not set" in both the list and the record; and a label
 * for every action and target type the server records (read out of the Go code in internal/).
 */
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { auditChange, changedRows, changeSummary, valueText } from "../components/audit/changes";
import { actionGroups, actionLabel, actionLabels, areaOf, entryTitle, reasonLine, targetTypeLabel, targetTypeLabels } from "../components/audit/labels";

const internal = path.resolve(__dirname, "../../../internal");

/** The Go sources the server is built from: no tests, no generated code. */
function goSources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((d) => {
    const p = path.join(dir, d.name);
    if (d.isDirectory()) return d.name === "dbgen" || d.name === "apitypes" ? [] : goSources(p);
    return d.name.endsWith(".go") && !d.name.endsWith("_test.go") ? [p] : [];
  });
}

const sources = goSources(internal).map((f) => readFileSync(f, "utf8"));
const matches = (re: RegExp) => [...new Set(sources.flatMap((src) => [...src.matchAll(re)].map((m) => m[1]!)))].sort();

/** Dotted string literals in the Go code that aren't audit actions. */
const notActions = new Set([
  "ca.crt",
  "index.html",
  // Telemetry (internal/tracing, internal/metrics): span and attribute names, not audit actions.
  "agent.runtime_request", "agent.wake", "mcp.upstream", "model.upstream",
  "http.route", "pdp.launcher", "pdp.listener", "pdp.model", "pdp.request_id", "service.name", "service.version",
]);
/** Every action recorded at v0.1.0 (the scan must find at least these: a floor for the scan itself). */
const knownActions = [
  "agent.create", "agent.delete", "agent.update",
  "agent_template.approve", "agent_template.create", "agent_template.publish_request", "agent_template.reject", "agent_template.unpublish", "agent_template.update", "agent_template.version",
  "api_key.create", "api_key.pepper_rehash", "api_key.revoke",
  "auth.login", "auth.login_denied", "auth.logout",
  "mcp_server.create", "mcp_server.delete", "mcp_server.update",
  "model.create", "model.delete", "model.update",
  "model_provider.create", "model_provider.delete", "model_provider.update",
  "oauth.client_register", "oauth.consent", "oauth.revoke", "oauth.token_issue", "oauth.trusted_clients_update",
  "platform.bootstrap_role", "platform.model_settings", "platform.personal_settings", "platform.secrets_rekey", "platform.user_reactivate", "platform.user_role_change", "platform.user_suspend", "platform.user_update",
  "service_account.create", "service_account.delete", "service_account.update",
  "service_account_key.create", "service_account_key.pepper_rehash", "service_account_key.revoke",
  "team.archive", "team.create", "team.invite_accept", "team.invite_create", "team.invite_revoke", "team.member_add", "team.member_leave", "team.member_remove", "team.member_role_change", "team.owner_assign", "team.personal_create", "team.personal_limits", "team.unarchive", "team.update",
  "thread.admin_read",
];

describe("audit labels for what the server records", () => {
  const actions = matches(/"([a-z][a-z_]*\.[a-z][a-z_]*)"/g).filter((a) => !notActions.has(a));

  it("finds the server's actions in internal/", () => {
    expect(actions).toEqual(expect.arrayContaining(knownActions));
  });

  it("has a label and an area for every action", () => {
    expect(actions.filter((a) => !actionLabels[a])).toEqual([]);
    expect(actions.filter((a) => !areaOf(a))).toEqual([]);
    for (const a of knownActions) expect(actionLabel(a)).toBe(actionLabels[a]);
    expect(actionLabel("oauth.token_issue")).toBe("Issued app token");
    expect(actionLabel("agent.create")).toBe("Deployed agent");
  });

  it("has a label for every target type", () => {
    const types = [...new Set([...matches(/TargetType:\s*"([a-z_]+)"/g), ...matches(/Audit\((?:"[^"]*"|[A-Za-z_][\w(), ]*?),\s*"([a-z_]+)"/g)])].sort();
    expect(types).toEqual(expect.arrayContaining(["agent", "agent_template", "agent_thread", "mcp_server", "model", "model_provider", "oauth_grant", "team", "user"]));
    expect(types.filter((t) => !targetTypeLabels[t])).toEqual([]);
  });

  it("puts every group's prefix in one area only", () => {
    const prefixes = actionGroups.map((g) => g.prefix);
    expect(new Set(prefixes).size).toBe(prefixes.length);
    for (const a of actions) expect(actionGroups.filter((g) => a.startsWith(g.prefix))).toHaveLength(1);
  });
});

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

  it("says a platform admin read a private conversation, with the reason", () => {
    const e = { action: "thread.admin_read", after: null, targetLabel: null, metadata: { reason: "  Support ticket 4521 ", visibility: "private", personal: false } };
    expect(actionLabel(e.action, e)).toBe("Read a private conversation");
    expect(entryTitle(e)).toBe("Read a private conversation");
    expect(reasonLine(e)).toBe("Reason: “Support ticket 4521”");
    expect(areaOf(e.action)).toBe("Conversations");
    expect(targetTypeLabel("agent_thread")).toBe("Conversation");
    // A personal workspace's conversations are its owner's, shared or not; a shared team's shared one isn't private.
    expect(actionLabel(e.action, { metadata: { visibility: "shared", personal: true } })).toBe("Read a private conversation");
    expect(actionLabel(e.action, { metadata: { visibility: "shared", personal: false } })).toBe("Read a team conversation");
    expect(reasonLine({ metadata: {} })).toBeUndefined();
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
