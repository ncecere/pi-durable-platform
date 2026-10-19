package httpapi_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// The `server` label of the MCP proxy metrics is the slug of a platform
// (catalog) server only: a team's own server is "team" and one an app gave a
// thread is "app", whatever slug the team or the app chose, so teams cannot
// fill the label guard (64 values) and push the platform's servers to "other".
func TestMCPMetricsLabelOnlyPlatformServersByName(t *testing.T) {
	var m *metrics.Metrics
	app := newTestAppDeps(t, func(c *config.Config) { c.Agents.ClientMCPHosts = "http://127.0.0.1:*" },
		testutil.Logger(), func(d *httpapi.Deps) { m = d.Metrics })
	e := newTeamEnvOn(t, app)
	scrape := httptest.NewServer(m.NewServer("").Handler)
	t.Cleanup(scrape.Close)

	catalog := newFakeMCP(t, "catalog", "Bearer catalog-secret")
	teamFake := newFakeTLSMCP(t, "team", teamMCPSecret)
	user := newFakeMCP(t, "user", "Bearer user-token")
	useTeamNetwork(t, app, teamFake.srv, map[string]string{"public.tools.test": "93.184.216.34"})

	var platform apitypes.McpServer
	code, ec := e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Fake Tools", "url": catalog.srv.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer catalog-secret"}, "visibility": "teams", "teams": []string{"tools"}}, &platform, nil)
	mustCode(t, "add the platform server", code, ec, 201, "")
	var own apitypes.McpServer
	code, ec = e.owner.call("POST", toolsTeam+"/mcp-servers/own", map[string]any{"name": "Evil Team Slug", "url": "https://public.tools.test/mcp",
		"headers": map[string]string{"Authorization": teamMCPSecret}}, &own, nil)
	mustCode(t, "add the team's server", code, ec, 201, "")

	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "both", "name": "Both", "instructions": "Use tools.",
		"clientMcp": true, "mcp": []any{map[string]any{"serverId": platform.Id}, map[string]any{"serverId": own.Id}}}, &tpl, nil)
	mustCode(t, "a template with both servers", code, ec, 201, "")
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Label bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	cred := e.scalarSpecKey(e.spec(ag.Id))
	base := "/v1/agents/" + ag.Id.String()

	// An app gives a thread a server of its own.
	code, ec = e.member.call("PUT", base+"/threads/t2/mcp-servers", map[string]any{"servers": []any{
		map[string]any{"name": "App Chosen Name", "url": user.srv.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer user-token"}}}}, nil, nil)
	mustCode(t, "the app's server", code, ec, 200, "")
	threadServer := e.scalar(`SELECT id::text FROM thread_mcp_servers WHERE agent_id = $1`, ag.Id)

	for _, c := range []struct{ kind, id, want string }{
		{"server", platform.Id.String(), "catalog echo"},
		{"server", own.Id.String(), "team echo"},
		{"thread", threadServer, "user echo"},
	} {
		if code, out := mcpProxyCallKind(t, app.GatewayURL, c.kind, c.id, cred, "echo"); code != 200 || !strings.Contains(out, c.want) {
			t.Fatalf("%s %s: %d %s", c.kind, c.id, code, out)
		}
	}

	s := metricstest.Fetch(t, scrape.URL+"/metrics")
	for _, server := range []string{platform.Slug, metrics.ServerTeam, metrics.ServerApp} {
		if got := s.Sum("pdp_mcp_tool_calls_total", "server", server, "result", "ok"); got != 1 {
			t.Errorf("pdp_mcp_tool_calls_total{server=%q} = %v, want 1", server, got)
		}
		if got := s.Sum("pdp_mcp_proxy_requests_total", "server", server, "code", "2xx"); got != 1 {
			t.Errorf("pdp_mcp_proxy_requests_total{server=%q} = %v, want 1", server, got)
		}
	}
	// What the team and the app chose never reaches a label.
	for _, chosen := range []string{own.Slug, "evil_team_slug", "app_chosen_name", "user_tools"} {
		if strings.Contains(s.Text, fmt.Sprintf("server=%q", chosen)) {
			t.Errorf("the scrape has a series labelled with the team's or app's server name %q", chosen)
		}
	}
}
