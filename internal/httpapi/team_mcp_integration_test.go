package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Team MCP servers are reached through the address guard, as team
// providers are: these tests use useTeamNetwork (team_models_integration_test.go),
// which resolves test host names from a table and sends approved
// connections to a local https fake MCP server.

const teamMCPSecret = "Bearer team-mcp-secret"

// mcpProxyCall calls a tool through the gateway's MCP proxy as an agent and
// returns the status and body.
func mcpProxyCall(t *testing.T, gatewayURL, serverID, cred, tool string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", gatewayURL+"/mcp-proxy/server/"+serverID,
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+tool+`","arguments":{"text":"hi"}}}`)))
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

// setTeamServiceSettings sets the model settings, with the MCP switch.
func (e *teamEnv) setTeamServiceSettings(providers, mcpServers bool, networks ...string) apitypes.ModelSettings {
	e.t.Helper()
	var cur, out apitypes.ModelSettings
	if code := e.admin.get("/v1/admin/settings/models", &cur); code != 200 {
		e.t.Fatalf("model settings: %d", code)
	}
	if networks == nil {
		networks = []string{}
	}
	code, ec := e.admin.call("PUT", "/v1/admin/settings/models", map[string]any{"teamsMayAddProviders": providers,
		"teamsMayAddMcpServers": mcpServers, "privateNetworks": networks}, &out, ifMatch(cur.Revision))
	mustCode(e.t, "set model settings", code, ec, 200, "")
	return out
}

func (e *teamEnv) canAddMCPServers(s *session, team string) bool {
	e.t.Helper()
	var l apitypes.WorkspaceLimits
	if code := s.get("/v1/teams/"+team+"/limits", &l); code != 200 || l.CanAddMcpServers == nil {
		e.t.Fatalf("limits of %s: %d %+v", team, code, l)
	}
	return *l.CanAddMcpServers
}

func findMCPServer(list []apitypes.McpServer, id string) *apitypes.McpServer {
	for i := range list {
		if list[i].Id.String() == id {
			return &list[i]
		}
	}
	return nil
}

// A team's own MCP servers end to end: the address guard and the platform's
// private networks decide where they may be; the team's owners and admins
// manage them (in a browser session), its members read them; they are the
// team's only (listing, templates, agents, the gateway's proxy, the
// catalog); platform admins see and govern them; deleting the team takes
// them along, with the templates that use them.
func TestTeamMCPServers(t *testing.T) {
	e := newTeamEnv(t)
	fake := newFakeTLSMCP(t, "team", teamMCPSecret)
	platformFake := newFakeMCP(t, "platform", "Bearer platform-secret")
	hosts := useTeamNetwork(t, e.app, fake.srv, map[string]string{
		"mcp.tools.test":    "10.20.30.40",   // a private address
		"public.tools.test": "93.184.216.34", // a public one
	})
	const own = toolsTeam + "/mcp-servers/own"
	body := func(name, u string) map[string]any {
		return map[string]any{"name": name, "url": u, "headers": map[string]string{"Authorization": teamMCPSecret}}
	}
	with := func(m map[string]any, k string, v any) map[string]any { m[k] = v; return m }

	// The settings: teams may add MCP servers by default.
	var settings apitypes.ModelSettings
	if code := e.auditor.get("/v1/admin/settings/models", &settings); code != 200 || settings.TeamsMayAddMcpServers == nil || !*settings.TeamsMayAddMcpServers {
		t.Fatalf("settings: %d %+v", code, settings)
	}

	// The URL must be https, proxy mode, on an address teams may use; the
	// server is the team's only.
	code, ec := e.owner.call("POST", own, body("Private", "https://mcp.tools.test/mcp"), nil, nil)
	mustCode(t, "a private address not allowed yet", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", own, body("Loopback", "https://"+fake.srv.Listener.Addr().String()+"/mcp"), nil, nil)
	mustCode(t, "a loopback address", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", own, body("Metadata", "https://169.254.169.254/mcp"), nil, nil)
	mustCode(t, "the metadata address", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", own, body("Plain", "http://public.tools.test/mcp"), nil, nil)
	mustCode(t, "plain http", code, ec, 400, "invalid_url")
	code, ec = e.owner.call("POST", own, body("Unknown", "https://nowhere.tools.test/mcp"), nil, nil)
	mustCode(t, "an unknown host", code, ec, 422, "address_unresolved")
	code, ec = e.owner.call("POST", own, with(body("Direct", "https://public.tools.test/mcp"), "accessMode", "direct"), nil, nil)
	mustCode(t, "direct mode", code, ec, 400, "invalid_access_mode")
	code, ec = e.owner.call("POST", own, with(body("All", "https://public.tools.test/mcp"), "visibility", "all"), nil, nil)
	mustCode(t, "for every team", code, ec, 400, "team_mcp_visibility")
	code, ec = e.owner.call("POST", own, with(body("Other", "https://public.tools.test/mcp"), "teams", []string{"other"}), nil, nil)
	mustCode(t, "for another team", code, ec, 400, "team_mcp_visibility")
	code, ec = e.owner.call("POST", own, with(body("Personal", "https://public.tools.test/mcp"), "personal", true), nil, nil)
	mustCode(t, "for personal workspaces", code, ec, 400, "team_mcp_visibility")

	// Platform admins allow the private network (the one list for every
	// kind of team service); the owner adds the server and its tools are read.
	e.setModelSettings(true, "10.20.0.0/16")
	var srv apitypes.McpServer
	code, raw := e.owner.raw("POST", own, body("Team Tools", "https://mcp.tools.test/mcp"), nil)
	if code != 201 || strings.Contains(string(raw), "team-mcp-secret") {
		t.Fatalf("create team server: %d %s", code, raw)
	}
	mustDecode(t, raw, &srv)
	if srv.Team == nil || srv.Team.Slug != "tools" || srv.Team.Kind != "team" || srv.AccessMode != "proxy" || srv.Slug != "team_tools" ||
		len(srv.Tools) != 2 || srv.ToolsError != nil || !srv.HasHeaders || srv.Visibility != "teams" || srv.Personal || srv.Teams != nil {
		t.Fatalf("team server %+v", srv)
	}
	sid := srv.Id.String()
	sbase := own + "/" + sid

	// Names are unique per owner; slugs across every owner (they name tools).
	code, ec = e.owner.call("POST", own, with(body("team TOOLS", "https://public.tools.test/mcp"), "slug", "other_slug"), nil, nil)
	mustCode(t, "the same name in the team", code, ec, 409, "mcp_server_name_taken")
	code, ec = e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Platform", "slug": "team_tools",
		"url": platformFake.srv.URL + "/mcp"}, nil, nil)
	mustCode(t, "a platform server with the team server's slug", code, ec, 409, "mcp_server_name_taken")
	var platformSrv apitypes.McpServer
	code, ec = e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Team Tools", "slug": "plat_tools",
		"url": platformFake.srv.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer platform-secret"}}, &platformSrv, nil)
	mustCode(t, "a platform server with the same name", code, ec, 201, "")
	if platformSrv.Team != nil {
		t.Fatalf("platform server has a team: %+v", platformSrv.Team)
	}
	code, ec = e.owner.call("POST", own, with(body("Mine", "https://public.tools.test/mcp"), "slug", "plat_tools"), nil, nil)
	mustCode(t, "a team server with a platform server's slug", code, ec, 409, "mcp_server_name_taken")

	// Members read; only owners and admins manage, in a browser session.
	var list []apitypes.McpServer
	if code := e.member.get(own, &list); code != 200 || len(list) != 1 || list[0].Id != srv.Id {
		t.Fatalf("member lists: %d %+v", code, list)
	}
	code, ec = e.member.call("POST", own, body("Mine", "https://public.tools.test/mcp"), nil, nil)
	mustCode(t, "a member adds a server", code, ec, 403, "forbidden")
	code, ec = e.editor.call("PATCH", sbase, map[string]any{"enabled": false}, nil, ifMatch(srv.Revision))
	mustCode(t, "an editor changes it", code, ec, 403, "forbidden")
	code, ec = e.editor.call("POST", sbase+"/refresh", nil, nil, nil)
	mustCode(t, "an editor refreshes it", code, ec, 403, "forbidden")
	code, ec = e.editor.call("DELETE", sbase, nil, nil, nil)
	mustCode(t, "an editor deletes it", code, ec, 403, "forbidden")
	sa := createSA(t, e.owner, map[string]any{"name": "bot", "role": "editor"})
	bot := keyClient{t, e.app.URL, createSAKey(t, e.owner, sa.Id.String(), map[string]any{"name": "k"}).Secret}
	code, ec = bot.call("POST", own, body("Bot's", "https://public.tools.test/mcp"), nil)
	mustCode(t, "a service account key adds a server", code, ec, 401, "")
	code, ec = bot.call("GET", own, nil, nil)
	mustCode(t, "a service account key lists own servers", code, ec, 401, "")

	// Another team, or the platform's server through a team route, is not found.
	code, ec = e.outsider.call("GET", own, nil, nil, nil)
	mustCode(t, "another team's server list", code, ec, 404, "team_not_found")
	code, ec = e.outsider.call("GET", "/v1/teams/other/mcp-servers/own/"+sid, nil, nil, nil)
	mustCode(t, "another team's server through one's own team", code, ec, 404, "mcp_server_not_found")
	code, ec = e.outsider.call("DELETE", "/v1/teams/other/mcp-servers/own/"+sid, nil, nil, nil)
	mustCode(t, "deleting another team's server", code, ec, 404, "mcp_server_not_found")
	code, ec = e.outsider.call("POST", "/v1/teams/other/mcp-servers/own/"+sid+"/refresh", nil, nil, nil)
	mustCode(t, "refreshing another team's server", code, ec, 404, "mcp_server_not_found")
	code, ec = e.owner.call("GET", own+"/"+platformSrv.Id.String(), nil, nil, nil)
	mustCode(t, "the platform's server through a team route", code, ec, 404, "mcp_server_not_found")
	code, ec = e.owner.call("PATCH", own+"/"+platformSrv.Id.String(), map[string]any{"enabled": false}, nil, ifMatch(platformSrv.Revision))
	mustCode(t, "changing the platform's server through a team route", code, ec, 404, "mcp_server_not_found")

	// The owner refreshes its tools and changes it.
	code, ec = e.owner.call("POST", sbase+"/refresh", nil, &srv, nil)
	mustCode(t, "refresh", code, ec, 200, "")
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"visibility": "all"}, nil, ifMatch(srv.Revision))
	mustCode(t, "publishing it to everyone", code, ec, 400, "team_mcp_visibility")
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"accessMode": "direct"}, nil, ifMatch(srv.Revision))
	mustCode(t, "making it direct", code, ec, 400, "invalid_access_mode")
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"description": "The team's tools"}, &srv, ifMatch(srv.Revision))
	mustCode(t, "describe it", code, ec, 200, "")

	// Available to the team only (its members and service accounts),
	// first, with its owner; never to another team.
	var mine, theirs []apitypes.McpServer
	e.member.get(toolsTeam+"/mcp-servers", &mine)
	e.outsider.get("/v1/teams/other/mcp-servers", &theirs)
	if len(mine) != 2 || mine[0].Id != srv.Id || mine[0].Team == nil || mine[0].Team.Slug != "tools" || mine[1].Team != nil ||
		findMCPServer(theirs, sid) != nil || findMCPServer(theirs, platformSrv.Id.String()) == nil {
		t.Fatalf("available: tools %+v, other %+v", mine, theirs)
	}
	if code, _ := bot.call("GET", toolsTeam+"/mcp-servers", nil, &mine); code != 200 || findMCPServer(mine, sid) == nil {
		t.Fatalf("a service account lists the team's servers: %d %+v", code, mine)
	}

	// Templates: the team's may attach it, another team's and platform
	// templates may not, and a template attaching it stays out of the catalog.
	attach := []any{map[string]any{"serverId": srv.Id, "tools": []string{"echo"}}}
	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates",
		map[string]any{"slug": "own", "name": "Own", "instructions": "Use tools.", "mcp": attach}, &tpl, nil)
	mustCode(t, "a team template with the team's server", code, ec, 201, "")
	code, ec = e.outsider.call("POST", "/v1/teams/other/agent-templates",
		map[string]any{"slug": "x", "name": "X", "instructions": "Hi.", "mcp": attach}, nil, nil)
	mustCode(t, "another team's template with it", code, ec, 409, "mcp_server_unavailable")
	code, ec = e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "p", "name": "P", "instructions": "Hi.", "mcp": attach}, nil, nil)
	mustCode(t, "a platform template with it", code, ec, 409, "team_mcp_server")
	code, ec = e.owner.call("POST", "/v1/agent-templates/"+tpl.Id.String()+"/publish", nil, nil, nil)
	mustCode(t, "publishing a template with it", code, ec, 409, "team_mcp_server")
	var pub apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "pub", "name": "Pub", "instructions": "Hi."}, &pub, nil)
	mustCode(t, "a template without it", code, ec, 201, "")
	code, ec = e.owner.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/publish", nil, nil, nil)
	mustCode(t, "ask to publish it", code, ec, 200, "")
	code, ec = e.editor.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/versions",
		map[string]any{"instructions": "Hi.", "mcp": attach}, nil, nil)
	mustCode(t, "a pending template's version with it", code, ec, 409, "team_mcp_server")
	e.scalar(`INSERT INTO template_version_mcp (template_id, version, server_id) VALUES ($1, 1, $2) RETURNING server_id::text`, pub.Id, srv.Id)
	code, ec = e.admin.call("POST", "/v1/agent-templates/"+pub.Id.String()+"/review", map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "approving a template with it", code, ec, 409, "team_mcp_server")

	// An agent of the team gets it through the gateway (never its URL or
	// header), and calls its tool through the proxy.
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Own bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	spec := e.spec(ag.Id)
	if env := spec.Env["MCP_SERVERS"]; !strings.Contains(env, e.app.GatewayURL+"/mcp-proxy/server/"+sid) ||
		strings.Contains(env, "tools.test") || strings.Contains(env, "team-mcp-secret") || strings.Contains(env, "secret_op") {
		t.Fatalf("MCP_SERVERS %s", env)
	}
	cred := e.scalarSpecKey(spec)
	if code, out := mcpProxyCall(t, e.app.GatewayURL, sid, cred, "echo"); code != 200 || !strings.Contains(out, "team echo: hi") {
		t.Fatalf("proxied call: %d %s", code, out)
	}
	if code, out := mcpProxyCall(t, e.app.GatewayURL, sid, cred, "secret_op"); !strings.Contains(out, "may not use the tool secret_op") {
		t.Fatalf("secret_op through the proxy: %d %s", code, out)
	}
	if auths, calls := fake.seen(); len(calls) != 1 || calls[0] != "echo" || auths[0] != teamMCPSecret {
		t.Fatalf("team server saw %v %v", auths, calls)
	}
	// The call is logged for the team, under the server's name.
	if got := e.scalar(`SELECT format('%s/%s/%s', c.server_name, c.tool, t.slug) FROM mcp_calls c JOIN teams t ON t.id = c.team_id
		WHERE c.server_id = $1`, srv.Id); got != "Team Tools/echo/tools" {
		t.Fatalf("mcp_calls %q", got)
	}

	// Another team's agent can't, even if its template version named it.
	var other apitypes.Agent
	code, ec = e.outsider.call("POST", "/v1/teams/other/agents", map[string]any{"name": "Other bot"}, &other, nil)
	mustCode(t, "another team's agent", code, ec, 201, "")
	casey := authz.Actor{UserID: e.userID("casey@localhost"), PlatformRole: authz.PlatformNone}
	otherSpec, err := e.app.Agents.Spec(context.Background(), casey, other.Id)
	if err != nil {
		t.Fatal(err)
	}
	e.scalar(`INSERT INTO template_version_mcp (template_id, version, server_id)
		SELECT template_id, template_version, $2 FROM agents WHERE id = $1 RETURNING server_id::text`, other.Id, srv.Id)
	if code, out := mcpProxyCall(t, e.app.GatewayURL, sid, otherSpec.Env["PLATFORM_KEY"], "echo"); code != 403 ||
		errorCode([]byte(out)) != "mcp_server_not_allowed" {
		t.Fatalf("another team's agent through the proxy: %d %s", code, out)
	}
	if s, err := e.app.Agents.Spec(context.Background(), casey, other.Id); err != nil || strings.Contains(s.Env["MCP_SERVERS"], sid) {
		t.Fatalf("another team's agent gets the server: %v %s", err, s.Env["MCP_SERVERS"])
	}
	e.scalar(`DELETE FROM template_version_mcp WHERE server_id = $1 AND template_id = (SELECT template_id FROM agents WHERE id = $2)
		RETURNING server_id::text`, srv.Id, other.Id)
	if _, calls := fake.seen(); len(calls) != 1 {
		t.Fatalf("another team's agent reached the server: %v", calls)
	}

	// Who may add one now (the UI's button).
	if !e.canAddMCPServers(e.owner, "tools") || e.canAddMCPServers(e.member, "tools") || e.canAddMCPServers(e.admin, "tools") {
		t.Fatal("canAddMcpServers: owner yes, member and platform admin no")
	}

	// Turned off, no new servers; existing ones keep working and changing;
	// the providers' switch is separate, and leaving the MCP switch out of
	// an update keeps it.
	if s := e.setTeamServiceSettings(true, false, "10.20.0.0/16"); s.TeamsMayAddMcpServers == nil || *s.TeamsMayAddMcpServers || !s.TeamsMayAddProviders {
		t.Fatalf("settings %+v", s)
	}
	code, ec = e.owner.call("POST", own, body("Second", "https://public.tools.test/mcp"), nil, nil)
	mustCode(t, "a server while turned off", code, ec, 409, "team_mcp_servers_off")
	if e.canAddMCPServers(e.owner, "tools") || !e.canAddProviders(e.owner, "tools") {
		t.Fatal("canAddMcpServers while turned off, or canAddProviders with it")
	}
	if s := e.setModelSettings(true, "10.20.0.0/16"); s.TeamsMayAddMcpServers == nil || *s.TeamsMayAddMcpServers {
		t.Fatalf("the MCP switch was not kept: %+v", s)
	}
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"name": "Team Tools"}, &srv, ifMatch(srv.Revision))
	mustCode(t, "changing it while turned off", code, ec, 200, "")
	if code, out := mcpProxyCall(t, e.app.GatewayURL, sid, cred, "echo"); code != 200 || !strings.Contains(out, "team echo") {
		t.Fatalf("proxied call while turned off: %d %s", code, out)
	}
	e.setTeamServiceSettings(true, true, "10.20.0.0/16")

	// The guard decides at every connection: once its network is no longer
	// allowed, the server is not reached.
	e.setModelSettings(true)
	code, ec = e.owner.call("POST", sbase+"/refresh", nil, nil, nil)
	mustCode(t, "refresh off the allowed networks", code, ec, 422, "address_not_allowed")
	if code, out := mcpProxyCall(t, e.app.GatewayURL, sid, cred, "echo"); code != 502 || errorCode([]byte(out)) != "address_not_allowed" {
		t.Fatalf("proxied call off the allowed networks: %d %s", code, out)
	}
	e.setModelSettings(true, "10.20.0.0/16")
	hosts.set("mcp.tools.test", "169.254.169.254") // DNS now points at the metadata service
	code, ec = e.owner.call("POST", sbase+"/refresh", nil, nil, nil)
	mustCode(t, "refresh after the name moved", code, ec, 422, "address_not_allowed")
	var got apitypes.McpServer
	if e.member.get(sbase, &got); got.ToolsError == nil || strings.Contains(*got.ToolsError, "169.254") || len(got.Tools) != 2 {
		t.Fatalf("tools error %+v", got)
	}
	hosts.set("mcp.tools.test", "10.20.30.40")
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"url": "https://127.0.0.1/mcp"}, nil, ifMatch(got.Revision))
	mustCode(t, "moving it to loopback", code, ec, 422, "address_not_allowed")
	code, ec = e.owner.call("POST", sbase+"/refresh", nil, &srv, nil)
	mustCode(t, "refresh back on the allowed network", code, ec, 200, "")
	if srv.ToolsError != nil {
		t.Fatalf("tools error after refresh: %v", *srv.ToolsError)
	}

	// Platform admins see every owner's servers and govern teams'.
	var all, plat, teamOnly []apitypes.McpServer
	e.auditor.get("/v1/admin/mcp-servers", &all)
	e.auditor.get("/v1/admin/mcp-servers?owner=platform", &plat)
	e.auditor.get("/v1/admin/mcp-servers?owner=tools", &teamOnly)
	if len(all) != 2 || all[0].Team != nil || all[1].Team == nil || all[1].Team.Slug != "tools" ||
		len(plat) != 1 || plat[0].Id != platformSrv.Id || len(teamOnly) != 1 || teamOnly[0].Id != srv.Id {
		t.Fatalf("admin lists: all %+v, platform %+v, tools %+v", all, plat, teamOnly)
	}
	code, ec = e.auditor.call("GET", "/v1/admin/mcp-servers?owner=nope", nil, nil, nil)
	mustCode(t, "an unknown owner", code, ec, 404, "team_not_found")
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+sid, map[string]any{"accessMode": "direct"}, nil, ifMatch(srv.Revision))
	mustCode(t, "an admin makes a team server direct", code, ec, 400, "invalid_access_mode")
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+sid, map[string]any{"visibility": "all"}, nil, ifMatch(srv.Revision))
	mustCode(t, "an admin publishes a team server to everyone", code, ec, 400, "team_mcp_visibility")
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+sid, map[string]any{"enabled": false}, &srv, ifMatch(srv.Revision))
	mustCode(t, "an admin turns it off", code, ec, 200, "")
	if e.member.get(toolsTeam+"/mcp-servers", &mine); findMCPServer(mine, sid) != nil {
		t.Fatalf("a disabled team server is available: %+v", mine)
	}
	code, ec = e.owner.call("PATCH", sbase, map[string]any{"enabled": true}, &srv, ifMatch(srv.Revision))
	mustCode(t, "the owner turns it back on", code, ec, 200, "")

	// Deleting: refused while template versions use it.
	code, ec = e.owner.call("DELETE", sbase, nil, nil, nil)
	mustCode(t, "delete a server in use", code, ec, 409, "mcp_server_in_use")
	var spare apitypes.McpServer
	code, ec = e.owner.call("POST", own, body("Spare", "https://public.tools.test/mcp"), &spare, nil)
	mustCode(t, "a spare server", code, ec, 201, "")
	code, ec = e.owner.call("DELETE", own+"/"+spare.Id.String(), nil, nil, nil)
	mustCode(t, "delete an unused server", code, ec, 200, "")

	// Audited in the team's log, never with the header.
	page := auditPage(t, e.editor, toolsTeam+"/audit", url.Values{"action": {"mcp_server.create"}})
	if entry := findAudit(t, page, "mcp_server.create", sid); entry.TargetLabel == nil || *entry.TargetLabel != "Team Tools" {
		t.Fatalf("team audit entry %+v", entry)
	}
	page = auditPage(t, e.editor, toolsTeam+"/audit", url.Values{"action": {"mcp_server.update"}})
	findAudit(t, page, "mcp_server.update", sid) // the platform admin's too
	page = auditPage(t, e.auditor, "/v1/admin/audit", url.Values{"action": {"platform.model_settings"}})
	if entry := findAudit(t, page, "platform.model_settings", "models"); entry.TargetLabel == nil || *entry.TargetLabel != "Model settings" {
		t.Fatalf("settings audit entry %+v", entry)
	}
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'platform.model_settings'
		AND after_state ? 'teamsMayAddMcpServers'`); got == "0" {
		t.Fatal("the MCP switch is not in the settings' audit")
	}
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action LIKE 'mcp_server%'
		AND (coalesce(before_state::text, '') || coalesce(after_state::text, '') || coalesce(metadata::text, '')) LIKE '%team-mcp-secret%'`); got != "0" {
		t.Fatalf("audit entries with the header: %s", got)
	}

	// Deleting the team takes its servers along, with the templates that use
	// them (which keep a server from being deleted on its own).
	if got := e.scalar(`WITH d AS (DELETE FROM teams WHERE slug = 'tools' RETURNING id) SELECT count(*)::text FROM d`); got != "1" {
		t.Fatalf("deleted %s teams", got)
	}
	if got := e.scalar(`SELECT count(*)::text FROM mcp_servers WHERE team_id IS NOT NULL`); got != "0" {
		t.Fatalf("%s team servers left", got)
	}
	if got := e.scalar(`SELECT count(*)::text FROM mcp_servers`); got != "1" {
		t.Fatalf("%s servers left, want the platform's", got)
	}
}

// Personal workspaces add their own MCP servers only when platform admins
// let them.
func TestPersonalWorkspaceMCPServers(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	fake := newFakeTLSMCP(t, "mine", teamMCPSecret)
	useTeamNetwork(t, app, fake.srv, map[string]string{"mcp.example.test": "93.184.216.34"})
	admin, user := app.signIn("admin"), app.signIn("user")
	var mine []apitypes.MyTeam
	if code := user.get("/v1/teams", &mine); code != 200 || len(mine) != 1 {
		t.Fatalf("teams %d %+v", code, mine)
	}
	ws := "/v1/teams/" + mine[0].Slug
	body := map[string]any{"name": "Mine", "url": "https://mcp.example.test/mcp", "headers": map[string]string{"Authorization": teamMCPSecret}}

	code, ec := user.call("POST", ws+"/mcp-servers/own", body, nil, nil)
	mustCode(t, "a personal server by default", code, ec, 409, "personal_workspace")
	var limits apitypes.WorkspaceLimits
	if user.get(ws+"/limits", &limits); limits.CanAddMcpServers == nil || *limits.CanAddMcpServers {
		t.Fatalf("limits %+v", limits)
	}

	var settings apitypes.PersonalWorkspaceSettings
	admin.get("/v1/admin/settings/personal-workspaces", &settings)
	if settings.OwnMcpServers == nil || *settings.OwnMcpServers {
		t.Fatalf("settings %+v", settings)
	}
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 5, "maxRunning": 2,
		"ownMcpServers": true}, &settings, ifMatch(settings.Revision))
	mustCode(t, "let personal workspaces add MCP servers", code, ec, 200, "")
	if settings.OwnMcpServers == nil || !*settings.OwnMcpServers || settings.OwnProviders == nil || *settings.OwnProviders {
		t.Fatalf("settings %+v", settings)
	}
	// Omitted, it is kept.
	code, ec = admin.call("PUT", "/v1/admin/settings/personal-workspaces", map[string]any{"enabled": true, "maxAgents": 5, "maxRunning": 2},
		&settings, ifMatch(settings.Revision))
	mustCode(t, "change other settings", code, ec, 200, "")
	if settings.OwnMcpServers == nil || !*settings.OwnMcpServers {
		t.Fatalf("ownMcpServers not kept: %+v", settings)
	}

	var srv apitypes.McpServer
	code, ec = user.call("POST", ws+"/mcp-servers/own", body, &srv, nil)
	mustCode(t, "a personal server when allowed", code, ec, 201, "")
	if srv.Team == nil || srv.Team.Kind != "personal" || len(srv.Tools) != 2 {
		t.Fatalf("personal server %+v", srv)
	}
	var avail []apitypes.McpServer
	if code := user.get(ws+"/mcp-servers", &avail); code != 200 || len(avail) != 1 || avail[0].Id != srv.Id {
		t.Fatalf("personal servers: %d %+v", code, avail)
	}
	if user.get(ws+"/limits", &limits); limits.CanAddMcpServers == nil || !*limits.CanAddMcpServers ||
		limits.CanAddProviders == nil || *limits.CanAddProviders {
		t.Fatalf("limits %+v", limits)
	}
}
