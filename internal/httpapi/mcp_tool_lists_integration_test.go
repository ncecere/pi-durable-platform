package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// newAppsMCP is an MCP server with an ordinary tool and one an MCP App keeps
// for its own interface (app-only); lists counts its tools/list requests.
func newAppsMCP(t *testing.T, lists *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		schema := map[string]any{"type": "object", "properties": map[string]any{}}
		switch msg.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "apps", "version": "1"}}
		case "tools/list":
			lists.Add(1)
			result = map[string]any{"tools": []any{
				map[string]any{"name": "plain", "inputSchema": schema},
				map[string]any{"name": "app_only", "inputSchema": schema, "_meta": map[string]any{"ui": map[string]any{"visibility": []string{"app"}}}},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A tool list stored before the platform read MCP Apps' visibility may hold
// app-only tools. At start, such lists are read again through the normal
// refresh (a team's server through the address guard), once: the app-only
// tool is no longer offered to agents. A list that can't be read again
// stays, with the error recorded, and is tried again at the next start.
func TestOldToolListsAreReadAgain(t *testing.T) {
	var lists atomic.Int32
	fake := newAppsMCP(t, &lists)
	app := newTestApp(t, nil)
	admin := app.signIn("admin")
	createTeam(t, admin, "tools", "user@localhost")

	var srv apitypes.McpServer
	c, ec := admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Apps", "url": fake.URL + "/mcp"}, &srv, nil)
	mustCode(t, "add the server", c, ec, 201, "")
	if len(srv.Tools) != 1 || srv.Tools[0].Name != "plain" {
		t.Fatalf("a new server's tools %+v", srv.Tools)
	}
	// The lists as a platform before v0.3 stored them: the app-only tool kept.
	const old = `[{"name": "plain", "inputSchema": {"type": "object", "properties": {}}},
		{"name": "app_only", "inputSchema": {"type": "object", "properties": {}}}]`
	exec(t, app, `UPDATE mcp_servers SET tools = $1 WHERE id = $2`, old, srv.Id)
	// A team's server stored the old way, at an address teams may not use (loopback).
	exec(t, app, `INSERT INTO mcp_servers (name, slug, url, team_id, visibility, tools)
		VALUES ('Team apps', 'team_apps', $1, (SELECT id FROM teams WHERE slug = 'tools'), 'teams', $2)`, fake.URL+"/mcp", old)
	before := lists.Load()
	// Teams' servers through the real address guard, without the development allowance.
	app.MCP.SetTeamNetwork(llm.TeamNetwork{})

	ctx := context.Background()
	if err := app.MCP.RecheckToolLists(ctx, testutil.Logger()); err != nil {
		t.Fatal(err)
	}
	var got apitypes.McpServer
	if c := admin.get("/v1/admin/mcp-servers/"+srv.Id.String(), &got); c != 200 || len(got.Tools) != 1 || got.Tools[0].Name != "plain" ||
		got.ToolsError != nil {
		t.Fatalf("after the upgrade step: %d %+v", c, got)
	}
	if n := scalar(t, app, `SELECT count(*)::text FROM mcp_servers, jsonb_array_elements(tools) e
		WHERE e->>'name' = 'app_only'`); n != "1" {
		t.Fatalf("app-only tools stored: %s (want only the unreachable team server's)", n)
	}
	if e := scalar(t, app, `SELECT coalesce(tools_error, '') FROM mcp_servers WHERE slug = 'team_apps'`); e == "" {
		t.Fatal("the team server's refusal wasn't recorded")
	}
	if n := lists.Load() - before; n != 1 {
		t.Fatalf("tools/list requests: %d (the team server must go through the address guard)", n)
	}
	// Read again once: only the list that couldn't be read is tried at the next start.
	if err := app.MCP.RecheckToolLists(ctx, testutil.Logger()); err != nil {
		t.Fatal(err)
	}
	if n := lists.Load() - before; n != 1 {
		t.Fatalf("tools/list requests after a second start: %d", n)
	}
}
