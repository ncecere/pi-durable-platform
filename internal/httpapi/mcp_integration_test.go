package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// fakeMCP is a Streamable HTTP MCP server (JSON replies) with two tools,
// echo and secret_op, that requires an Authorization header.
type fakeMCP struct {
	srv   *httptest.Server
	label string
	mu    sync.Mutex
	auth  []string
	calls []string
}

func newFakeMCP(t *testing.T, label, wantAuth string) *fakeMCP {
	f := &fakeMCP{label: label}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch msg.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s-1")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": label, "version": "1"}}
		case "tools/list":
			schema := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
			result = map[string]any{"tools": []any{
				map[string]any{"name": "echo", "description": "Echo the text", "inputSchema": schema},
				map[string]any{"name": "secret_op", "description": "Not for agents", "inputSchema": schema},
			}}
		case "tools/call":
			f.mu.Lock()
			f.auth, f.calls = append(f.auth, r.Header.Get("Authorization")), append(f.calls, msg.Params.Name)
			f.mu.Unlock()
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("%s echo: %v", label, msg.Params.Arguments["text"])}}}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMCP) seen() ([]string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auth...), append([]string(nil), f.calls...)
}

var useTool = regexp.MustCompile(`use (\S+)`)

// newToolCallingModel is an OpenAI chat-completions server: asked "use
// <tool>", it calls that tool with {"text": "hi"}; given a tool result, it
// answers "Tool said: <result>".
func newToolCallingModel(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"tool-model"}]}`)
			return
		}
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		last := body.Messages[len(body.Messages)-1]
		content := string(last.Content)
		var s string
		if json.Unmarshal(last.Content, &s) == nil {
			content = s
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(data string) { fmt.Fprintf(w, "data: %s\n\n", data); w.(http.Flusher).Flush() }
		if m := useTool.FindStringSubmatch(content); last.Role == "user" && m != nil {
			send(`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"` + m[1] + `","arguments":""}}]},"finish_reason":null}]}`)
			send(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\":\"hi\"}"}}]},"finish_reason":null}]}`)
			send(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		} else {
			reply, _ := json.Marshal("Tool said: " + content)
			send(`{"choices":[{"index":0,"delta":{"role":"assistant","content":` + string(reply) + `},"finish_reason":null}]}`)
			send(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		}
		send(`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
		send("[DONE]")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The MCP catalog end to end: a platform admin adds an MCP server (its
// header stays on the platform) and publishes it to a team; a template gives
// its agents one of its tools; the agent calls it through the platform's MCP
// proxy, which refuses the other tool; an app gives a thread its user's own
// MCP server with a message, on an allowed host only.
func TestMCPCatalog(t *testing.T) {
	catalog := newFakeMCP(t, "catalog", "Bearer catalog-secret")
	user := newFakeMCP(t, "user", "Bearer user-token")
	llm := newToolCallingModel(t)
	app := newTestApp(t, func(c *config.Config) { c.Agents.ClientMCPHosts = "http://127.0.0.1:*" })
	e := newTeamEnvOn(t, app)

	// Platform admins add MCP servers; header values are never returned.
	body := map[string]any{"name": "Fake Tools", "url": catalog.srv.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer catalog-secret"},
		"visibility": "teams", "teams": []string{"tools"}}
	code, ec := e.editor.call("POST", "/v1/admin/mcp-servers", body, nil, nil)
	mustCode(t, "an editor adds an MCP server", code, ec, 403, "")
	code, raw := e.admin.raw("POST", "/v1/admin/mcp-servers", body, nil)
	if code != 201 || strings.Contains(string(raw), "catalog-secret") {
		t.Fatalf("create: %d %s", code, raw)
	}
	var srv apitypes.McpServer
	mustDecode(t, raw, &srv)
	if srv.Slug != "fake_tools" || len(srv.Tools) != 2 || srv.ToolsError != nil || !srv.HasHeaders {
		t.Fatalf("server %+v", srv)
	}
	var list []apitypes.McpServer
	if code := e.auditor.get("/v1/admin/mcp-servers", &list); code != 200 || len(list) != 1 {
		t.Fatalf("auditor lists: %d", code)
	}
	if code := e.outsider.get("/v1/teams/other/mcp-servers", &list); code != 200 || len(list) != 0 {
		t.Fatalf("other team sees %d servers", len(list))
	}

	// A model that calls tools, published to everyone.
	var prov apitypes.ModelProvider
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "Tool LLM", "kind": "openai-compatible",
		"baseUrl": llm.URL + "/v1", "apiKey": "llm-key"}, &prov, nil)
	mustCode(t, "add the model provider", code, ec, 201, "")
	var model apitypes.Model
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "tool-model"}, &model, nil)
	mustCode(t, "add the model", code, ec, 201, "")

	// Templates: only the server's real tools; another team can't attach it.
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "bad", "name": "Bad", "instructions": "Hi.",
		"mcp": []any{map[string]any{"serverId": srv.Id, "tools": []string{"nope"}}}}, nil, nil)
	mustCode(t, "attach a tool the server doesn't have", code, ec, 400, "invalid_mcp_tool")
	code, ec = e.outsider.call("POST", "/v1/teams/other/agent-templates", map[string]any{"slug": "x", "name": "X", "instructions": "Hi.",
		"mcp": []any{map[string]any{"serverId": srv.Id}}}, nil, nil)
	mustCode(t, "another team attaches the server", code, ec, 409, "mcp_server_unavailable")
	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "mcp", "name": "MCP", "instructions": "Use tools.",
		"modelId": model.Id, "clientMcp": true, "mcp": []any{map[string]any{"serverId": srv.Id, "tools": []string{"echo"}}}}, &tpl, nil)
	mustCode(t, "a template with the echo tool", code, ec, 201, "")
	var versions []apitypes.AgentTemplateVersion
	e.member.get("/v1/agent-templates/"+tpl.Id.String()+"/versions", &versions)
	if len(versions) != 1 || versions[0].Mcp == nil || len(*versions[0].Mcp) != 1 || versions[0].ClientMcp == nil || !*versions[0].ClientMcp {
		t.Fatalf("versions %+v", versions)
	}
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "MCP bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()

	// The runtime reaches the server through the gateway, without its header.
	spec := e.spec(ag.Id)
	if !strings.Contains(spec.Env["MCP_SERVERS"], e.app.GatewayURL+"/mcp-proxy/server/"+srv.Id.String()) ||
		strings.Contains(spec.Env["MCP_SERVERS"], "catalog-secret") || strings.Contains(spec.Env["MCP_SERVERS"], "secret_op") {
		t.Fatalf("MCP_SERVERS %s", spec.Env["MCP_SERVERS"])
	}

	// The agent calls the catalog tool through the proxy.
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "use mcp__fake_tools__echo"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	v := waitReply(t, e.member, base+"/threads/t1", 4)
	if got := text(v.Messages[len(v.Messages)-1]); !strings.Contains(got, "catalog echo: hi") {
		t.Fatalf("reply %q (%+v)", got, v.Messages)
	}
	if auths, calls := catalog.seen(); len(calls) != 1 || calls[0] != "echo" || auths[0] != "Bearer catalog-secret" {
		t.Fatalf("catalog server saw %v %v", auths, calls)
	}
	if got := e.scalar(`SELECT format('%s/%s', tool, ok::text) FROM mcp_calls WHERE agent_id = $1`, ag.Id); got != "echo/true" {
		t.Fatalf("mcp_calls %q", got)
	}

	// The proxy refuses the tool the template left out.
	cred := strings.TrimPrefix(e.scalarSpecKey(spec), "")
	req, _ := http.NewRequest("POST", e.app.GatewayURL+"/mcp-proxy/server/"+srv.Id.String(),
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"secret_op","arguments":{}}}`)))
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(out), "may not use the tool secret_op") {
		t.Fatalf("secret_op through the proxy: %d %s", res.StatusCode, out)
	}
	if _, calls := catalog.seen(); len(calls) != 1 {
		t.Fatalf("secret_op reached the server: %v", calls)
	}

	// An app gives the thread its user's MCP server with a message.
	userServer := map[string]any{"name": "User Tools", "url": user.srv.URL + "/mcp", "headers": map[string]string{"Authorization": "Bearer user-token"}}
	code, ec = e.member.call("POST", base+"/threads/t2/messages", map[string]any{"text": "use mcpc__user_tools__echo",
		"mcpServers": []any{map[string]any{"name": "Evil", "url": "https://evil.example.com/mcp"}}}, nil, nil)
	mustCode(t, "a host not on the allowlist", code, ec, 400, "client_mcp_host_not_allowed")
	code, ec = e.member.call("POST", base+"/threads/t2/messages", map[string]any{"text": "use mcpc__user_tools__echo",
		"mcpServers": []any{userServer}}, nil, nil)
	mustCode(t, "send with the user's MCP server", code, ec, 202, "")
	v = waitReply(t, e.member, base+"/threads/t2", 4)
	if got := text(v.Messages[len(v.Messages)-1]); !strings.Contains(got, "user echo: hi") {
		t.Fatalf("reply %q (%+v)", got, v.Messages)
	}
	if auths, calls := user.seen(); len(calls) != 1 || auths[0] != "Bearer user-token" {
		t.Fatalf("user server saw %v %v", auths, calls)
	}
	var threadServers []apitypes.ThreadMcpServer
	code, raw = e.member.raw("GET", base+"/threads/t2/mcp-servers", nil, nil)
	if code != 200 || strings.Contains(string(raw), "user-token") {
		t.Fatalf("thread servers: %d %s", code, raw)
	}
	mustDecode(t, raw, &threadServers)
	if len(threadServers) != 1 || threadServers[0].Slug != "user_tools" {
		t.Fatalf("thread servers %+v", threadServers)
	}
	if got := e.scalar(`SELECT count(*)::text FROM thread_mcp_servers WHERE position('user-token' in encode(headers_enc, 'escape')) > 0`); got != "0" {
		t.Fatal("the user's token is stored in the clear")
	}
	// Removing them: the next message has no user tools.
	code, ec = e.member.call("PUT", base+"/threads/t2/mcp-servers", map[string]any{"servers": []any{}}, nil, nil)
	mustCode(t, "remove the thread's servers", code, ec, 200, "")

	// A template without clientMcp refuses app servers.
	var plain apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Plain"}, &plain, nil)
	mustCode(t, "deploy a plain agent", code, ec, 201, "")
	code, ec = e.member.call("PUT", "/v1/agents/"+plain.Id.String()+"/threads/t1/mcp-servers", map[string]any{"servers": []any{userServer}}, nil, nil)
	mustCode(t, "app servers on a template without clientMcp", code, ec, 409, "client_mcp_not_allowed")

	// Direct mode: the runtime gets the server's URL and header.
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"accessMode": "direct"}, &srv, ifMatch(srv.Revision))
	mustCode(t, "switch to direct", code, ec, 200, "")
	if env := e.spec(ag.Id).Env["MCP_SERVERS"]; !strings.Contains(env, catalog.srv.URL+"/mcp") || !strings.Contains(env, "catalog-secret") {
		t.Fatalf("direct MCP_SERVERS %s", env)
	}

	// A server in use is not deleted.
	code, ec = e.admin.call("DELETE", "/v1/admin/mcp-servers/"+srv.Id.String(), nil, nil, nil)
	mustCode(t, "delete a server in use", code, ec, 409, "mcp_server_in_use")
}

// scalarSpecKey is the agent's gateway credential, from its runtime spec.
func (e *teamEnv) scalarSpecKey(spec struct{ Env map[string]string }) string {
	var servers []struct {
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(spec.Env["MCP_SERVERS"]), &servers); err != nil || len(servers) == 0 {
		e.t.Fatalf("MCP_SERVERS %q", spec.Env["MCP_SERVERS"])
	}
	return strings.TrimPrefix(servers[0].Headers["Authorization"], "Bearer ")
}
