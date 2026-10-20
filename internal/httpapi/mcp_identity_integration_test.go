package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
)

// identityClaims are the identity token's own claims, as a server reads them.
type identityClaims struct {
	PrincipalType string `json:"principal_type"`
	Name          string `json:"name"`
	Team          struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Kind string `json:"kind"`
	} `json:"team"`
	ThreadVisibility string `json:"thread_visibility"`
	Person           *struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Email string `json:"email"`
	} `json:"person"`
	PersonPresent bool   `json:"person_present"`
	Tool          string `json:"tool"`
}

// identityRequest is one request the server received and what it made of
// the token.
type identityRequest struct {
	method   string // the JSON-RPC method, or the HTTP method
	auth     string
	raw      string // the token ("": none)
	verified bool
	err      error
	std      jwt.Claims
	claims   identityClaims
}

// identityMCP is an MCP server (one tool, whoami) that verifies the
// platform's identity tokens the way docs/mcp-identity.md tells server
// authors to: ES256, the key from the platform's JWKS (fetched for every
// request, so a rotation shows at once), iss, aud and the times.
type identityMCP struct {
	srv    *httptest.Server
	issuer string
	jwks   string
	mu     sync.Mutex
	aud    string
	seen   []identityRequest
}

func newIdentityMCP(t *testing.T, appURL string) *identityMCP {
	f := &identityMCP{issuer: appURL, jwks: appURL + mcpidentity.JWKSPath}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.aud = f.url()
	return f
}

func (f *identityMCP) url() string { return f.srv.URL + "/mcp" }

func (f *identityMCP) setAudience(aud string) {
	f.mu.Lock()
	f.aud = aud
	f.mu.Unlock()
}

func (f *identityMCP) keys() (jose.JSONWebKeySet, error) {
	var set jose.JSONWebKeySet
	res, err := http.Get(f.jwks)
	if err != nil {
		return set, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return set, fmt.Errorf("jwks: %d", res.StatusCode)
	}
	return set, json.NewDecoder(res.Body).Decode(&set)
}

// verify checks a token as a server would, at time at.
func (f *identityMCP) verify(raw, aud string, at time.Time) (jwt.Claims, identityClaims, error) {
	var std jwt.Claims
	var own identityClaims
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return std, own, err
	}
	if len(tok.Headers) != 1 || tok.Headers[0].Algorithm != "ES256" {
		return std, own, errors.New("not one ES256 signature")
	}
	set, err := f.keys()
	if err != nil {
		return std, own, err
	}
	matches := set.Key(tok.Headers[0].KeyID)
	if len(matches) == 0 {
		return std, own, errors.New("unknown kid")
	}
	if err := tok.Claims(matches[0].Key, &std, &own); err != nil {
		return std, own, err
	}
	return std, own, std.ValidateWithLeeway(jwt.Expected{Issuer: f.issuer, AnyAudience: jwt.Audience{aud}, Time: at}, 30*time.Second)
}

func (f *identityMCP) serve(w http.ResponseWriter, r *http.Request) {
	req := identityRequest{method: r.Method, auth: r.Header.Get("Authorization"), raw: r.Header.Get(mcpidentity.Header)}
	if r.Header.Get(mcpidentity.ThreadHeader) != "" {
		http.Error(w, "the runtime's thread header reached the server", http.StatusTeapot)
		return
	}
	var msg struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&msg)
		req.method = msg.Method
	}
	f.mu.Lock()
	aud := f.aud
	f.mu.Unlock()
	if req.raw != "" {
		req.std, req.claims, req.err = f.verify(req.raw, aud, time.Now())
		req.verified = req.err == nil
	}
	f.mu.Lock()
	f.seen = append(f.seen, req)
	f.mu.Unlock()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any
	switch msg.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "s-1")
		result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "ident", "version": "1"}}
	case "tools/list":
		result = map[string]any{"tools": []any{map[string]any{"name": "whoami", "description": "Who is calling",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}}}
	case "tools/call":
		who := "nobody"
		if req.claims.Person != nil {
			who = req.claims.Person.Sub
		}
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "you are " + who}}}
	default:
		result = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
}

// last is the latest request with the method, and how many there were.
func (f *identityMCP) last(t *testing.T, method string) identityRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.seen) - 1; i >= 0; i-- {
		if f.seen[i].method == method {
			return f.seen[i]
		}
	}
	t.Fatalf("the server got no %s", method)
	return identityRequest{}
}

func (f *identityMCP) all() []identityRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]identityRequest(nil), f.seen...)
}

// lockedBuffer collects the platform's logs (runtimes' output included).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// identityCall sends whoami to the gateway's MCP proxy as the agent's
// runtime would, naming thread ("": none) in PDP-Thread.
func identityCall(t *testing.T, gatewayURL string, serverID uuid.UUID, cred, thread string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", gatewayURL+"/mcp-proxy/server/"+serverID.String(),
		strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`))
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if thread != "" {
		req.Header.Set(mcpidentity.ThreadHeader, thread)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

// agentCredential is the agent's gateway credential, from its runtime spec
// as the person sees it.
func agentCredential(t *testing.T, e *teamEnv, email string, agentID uuid.UUID) string {
	t.Helper()
	spec, err := e.app.Agents.Spec(context.Background(), authz.Actor{UserID: e.userID(email), PlatformRole: authz.PlatformNone}, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return e.scalarSpecKey(struct{ Env map[string]string }{spec.Env})
}

type oidcIdentity struct{ iss, sub string }

func (e *teamEnv) oidcOf(email string) oidcIdentity {
	var id oidcIdentity
	if err := e.app.Pool.QueryRow(context.Background(), `SELECT oidc_issuer, oidc_subject FROM users WHERE email = $1`, email).Scan(&id.iss, &id.sub); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// wantPerson checks who a verified token names (nil: nobody).
func wantPerson(t *testing.T, what string, got identityRequest, person *oidcIdentity, present bool, visibility string) {
	t.Helper()
	if !got.verified {
		t.Fatalf("%s: the server couldn't verify the token (one sent: %v): %v", what, got.raw != "", got.err)
	}
	c := got.claims
	switch {
	case person == nil && c.Person != nil:
		t.Errorf("%s: names %+v, want nobody", what, *c.Person)
	case person != nil && (c.Person == nil || c.Person.Iss != person.iss || c.Person.Sub != person.sub):
		t.Errorf("%s: names %+v, want %+v", what, c.Person, *person)
	}
	if c.PersonPresent != present || c.ThreadVisibility != visibility || c.PrincipalType != "agent" {
		t.Errorf("%s: present %v, visibility %q, principal %q; want %v %q agent", what, c.PersonPresent, c.ThreadVisibility, c.PrincipalType, present, visibility)
	}
}

// The person behind an MCP tool call (docs/mcp-identity.md), end to end: a
// server receives a token for every proxied request, verifies it with the
// platform's JWKS, and sees the owner for a personal workspace's call, the
// creator for a private thread, nobody for a shared thread or a call without
// a thread, and nobody when a runtime names another agent's thread. The
// runtime names the thread, never the person; direct mode gets no token;
// keys rotate with an overlap; tokens and the server's secret never reach
// the logs.
func TestMCPIdentityToken(t *testing.T) {
	logs := &lockedBuffer{}
	app := newTestAppWith(t, nil, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), nil, true)
	e := newTeamEnvOn(t, app)
	ident := newIdentityMCP(t, app.URL)
	llm := newToolCallingModel(t)

	// The keys: published unauthenticated and cacheable.
	res, err := http.Get(app.URL + mcpidentity.JWKSPath)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "public, max-age=300" || res.Header.Get("Content-Type") != "application/jwk-set+json" {
		t.Fatalf("JWKS: %d %v", res.StatusCode, res.Header)
	}

	// A platform server, on for every team and personal workspaces; the
	// token is on by default, for its URL, without email.
	code, ec := e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Ident", "url": ident.url(),
		"headers": map[string]string{mcpidentity.Header: "forged"}}, nil, nil)
	mustCode(t, "a stored identity header", code, ec, 400, "invalid_headers")
	var srv apitypes.McpServer
	code, ec = e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Ident", "url": ident.url(),
		"headers": map[string]string{"Authorization": "Bearer ident-secret"}}, &srv, nil)
	mustCode(t, "add the server", code, ec, 201, "")
	if !srv.SendIdentity || srv.IdentityAudience != nil || srv.IdentityEmail || len(srv.Tools) != 1 {
		t.Fatalf("server %+v", srv)
	}
	// Reading its tools carries no token.
	if r := ident.last(t, "tools/list"); r.raw != "" {
		t.Fatal("the platform's tool list read carried a token")
	}

	var prov apitypes.ModelProvider
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "Tool LLM", "kind": "openai-compatible",
		"baseUrl": llm.URL + "/v1", "apiKey": "llm-key"}, &prov, nil)
	mustCode(t, "add the model provider", code, ec, 201, "")
	var model apitypes.Model
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "tool-model"}, &model, nil)
	mustCode(t, "add the model", code, ec, 201, "")
	var tpl apitypes.AgentTemplate
	code, ec = e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "ident", "name": "Ident", "instructions": "Use tools.",
		"modelId": model.Id, "mcp": []any{map[string]any{"serverId": srv.Id}}}, &tpl, nil)
	mustCode(t, "a catalog template with the server", code, ec, 201, "")

	var teamAgent, otherAgent, mine apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Team bot", "templateId": tpl.Id}, &teamAgent, nil)
	mustCode(t, "deploy a team agent", code, ec, 201, "")
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Other bot", "templateId": tpl.Id}, &otherAgent, nil)
	mustCode(t, "deploy another team agent", code, ec, 201, "")
	code, ec = e.owner.call("POST", "/v1/teams/"+personalSlug(t, e.owner)+"/agents", map[string]any{"name": "My bot", "templateId": tpl.Id}, &mine, nil)
	mustCode(t, "deploy in a personal workspace", code, ec, 201, "")
	if env := e.spec(teamAgent.Id).Env["MCP_SERVERS"]; !strings.Contains(env, `"sendThread":true`) {
		t.Fatalf("the runtime isn't told to name threads: %s", env)
	}
	blair, alex, owner := e.oidcOf("blair@localhost"), e.oidcOf("alex@localhost"), e.oidcOf("user@localhost")
	team := "/v1/agents/" + teamAgent.Id.String()

	// A private thread, through the real runtime: its creator, present.
	code, ec = e.member.call("POST", team+"/threads/p1/messages", map[string]any{"text": "use mcp__ident__whoami", "visibility": "private"}, nil, nil)
	mustCode(t, "a private thread", code, ec, 202, "")
	if v := waitReply(t, e.member, team+"/threads/p1", 4); !strings.Contains(text(v.Messages[len(v.Messages)-1]), "you are "+blair.sub) {
		t.Fatalf("reply %q; the server saw %d requests; logs:\n%s", text(v.Messages[len(v.Messages)-1]), len(ident.all()), grepLines(logs.String(), "identity"))
	}
	call := ident.last(t, "tools/call")
	wantPerson(t, "private thread", call, &blair, true, "private")
	c := call.claims
	if call.std.Subject != teamAgent.Id.String() || c.Name != "Team bot" || c.Team.Slug != "tools" || c.Team.Kind != "team" || c.Tool != "whoami" ||
		c.Person.Email != "" || call.std.ID == "" || call.std.Expiry.Time().Sub(call.std.IssuedAt.Time()) != mcpidentity.TokenTTL {
		t.Errorf("claims %+v %+v", call.std, c)
	}
	if call.auth != "Bearer ident-secret" {
		t.Errorf("the server's own Authorization header changed: %q", call.auth)
	}
	// The handshake carries a token too, naming nobody: the runtime names the thread on calls only.
	wantPerson(t, "handshake", ident.last(t, "initialize"), nil, false, "none")
	firstToken := call.raw

	// A shared thread: nobody.
	code, ec = e.member.call("POST", team+"/threads/s1/messages", map[string]any{"text": "use mcp__ident__whoami"}, nil, nil)
	mustCode(t, "a shared thread", code, ec, 202, "")
	waitReply(t, e.member, team+"/threads/s1", 4)
	wantPerson(t, "shared thread", ident.last(t, "tools/call"), nil, false, "shared")

	// A personal workspace: its owner, in any thread.
	ws := "/v1/agents/" + mine.Id.String()
	code, ec = e.owner.call("POST", ws+"/threads/w1/messages", map[string]any{"text": "use mcp__ident__whoami"}, nil, nil)
	mustCode(t, "a personal workspace's thread", code, ec, 202, "")
	waitReply(t, e.owner, ws+"/threads/w1", 4)
	wantPerson(t, "personal workspace", ident.last(t, "tools/call"), &owner, true, "shared")
	if c := ident.last(t, "tools/call").claims; c.Team.Kind != "personal" {
		t.Errorf("personal workspace team %+v", c.Team)
	}

	// Calls as a runtime would make them, straight to the gateway.
	teamCred, otherCred := agentCredential(t, e, "blair@localhost", teamAgent.Id), agentCredential(t, e, "blair@localhost", otherAgent.Id)
	mineCred := agentCredential(t, e, "user@localhost", mine.Id)
	proxied := func(what, cred, thread string, person *oidcIdentity, present bool, visibility string) identityRequest {
		t.Helper()
		if code, out := identityCall(t, app.GatewayURL, srv.Id, cred, thread); code != 200 {
			t.Fatalf("%s: %d %s", what, code, out)
		}
		got := ident.last(t, "tools/call")
		wantPerson(t, what, got, person, present, visibility)
		return got
	}
	proxied("no thread (background work)", teamCred, "", nil, false, "none")
	proxied("a personal workspace without a thread", mineCred, "", &owner, false, "none")
	proxied("the private thread again", teamCred, "p1", &blair, true, "private")
	// A personal workspace thread working on a triggered run (the agent's own
	// reminder, say): the owner, not present.
	proxied("a personal workspace's thread", mineCred, "w1", &owner, true, "shared")
	e.scalar(`INSERT INTO trigger_runs (fire_id, agent_id, team_id, trigger_name, trigger_kind, trigger_source, fired_by, thread_id, status, created_at)
		SELECT 'sc_identity_1', id, team_id, 'Reminder', 'schedule', 'agent', 'schedule', 'w1', 'running', now() FROM agents WHERE id = $1 RETURNING fire_id`, mine.Id)
	proxied("a personal workspace's thread in a triggered run", mineCred, "w1", &owner, false, "shared")
	e.scalar(`UPDATE trigger_runs SET status = 'succeeded' WHERE fire_id = 'sc_identity_1' RETURNING fire_id`)
	proxied("the thread after the run", mineCred, "w1", &owner, true, "shared")

	// Another agent's private thread: alex starts one on the other agent;
	// the first agent's runtime claims it and gets nobody.
	code, ec = e.editor.call("POST", "/v1/agents/"+otherAgent.Id.String()+"/threads/a1/messages", map[string]any{"text": "hello", "visibility": "private"}, nil, nil)
	mustCode(t, "alex's private thread", code, ec, 202, "")
	waitReply(t, e.editor, "/v1/agents/"+otherAgent.Id.String()+"/threads/a1", 2)
	proxied("the thread's own agent", otherCred, "a1", &alex, true, "private")
	proxied("another agent's thread", teamCred, "a1", nil, false, "shared")
	if code, out := identityCall(t, app.GatewayURL, srv.Id, teamCred, "bad id!"); code != 400 || !strings.Contains(out, "invalid_thread") {
		t.Fatalf("a malformed thread: %d %s", code, out)
	}

	// A private thread a service account started names nobody.
	sa := createSA(t, e.owner, map[string]any{"name": "chat-app", "role": "member"})
	e.scalar(`INSERT INTO agent_threads (agent_id, thread_id, title, created_by_service, visibility) VALUES ($1, 'sa1', 'App', $2, 'private') RETURNING thread_id`,
		teamAgent.Id, sa.Id)
	proxied("a service account's private thread", teamCred, "sa1", nil, false, "private")

	// Per server: an audience and the email.
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"identityAudience": "has space"}, nil, ifMatch(srv.Revision))
	mustCode(t, "an audience with a space", code, ec, 400, "invalid_identity_audience")
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"identityAudience": "https://portal.example.com",
		"identityEmail": true}, &srv, ifMatch(srv.Revision))
	mustCode(t, "set an audience and the email", code, ec, 200, "")
	if srv.IdentityAudience == nil || *srv.IdentityAudience != "https://portal.example.com" || !srv.IdentityEmail {
		t.Fatalf("server %+v", srv)
	}
	if code, _ := identityCall(t, app.GatewayURL, srv.Id, mineCred, ""); code != 200 {
		t.Fatal("call")
	}
	if got := ident.last(t, "tools/call"); got.verified || !strings.Contains(fmt.Sprint(got.err), "aud") {
		t.Fatalf("a token for another audience verified: %v", got.err)
	}
	ident.setAudience("https://portal.example.com")
	got := proxied("with email, for the portal", mineCred, "", &owner, false, "none")
	if got.claims.Person.Email != "user@localhost" {
		t.Errorf("email %q", got.claims.Person.Email)
	}
	if !strings.Contains(e.scalar(`SELECT after_state::text FROM audit_log WHERE action = 'mcp_server.update' ORDER BY id DESC LIMIT 1`), `"identityEmail": true`) {
		t.Error("the change isn't audited")
	}

	// Expiry: a server checking later than five minutes refuses the token.
	if _, _, err := ident.verify(got.raw, "https://portal.example.com", time.Now().Add(mcpidentity.TokenTTL+time.Minute)); err == nil {
		t.Error("an expired token verified")
	}

	// Rotation: a key published before it signs, then one signing at once.
	// Tokens of the old and the new key verify during the overlap.
	oldKid := kidOf(t, got.raw)
	rot, err := app.Identity.Rotate(context.Background(), mcpidentity.RotateOptions{Prepublish: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if kids := jwksKids(t, ident); len(kids) != 2 || !kids[rot.Kid] || !kids[oldKid] {
		t.Fatalf("published kids %v", kids)
	}
	got = proxied("before the new key signs", mineCred, "", &owner, false, "none")
	if kidOf(t, got.raw) != oldKid {
		t.Error("a prepublished key signed early")
	}
	now, err := app.Identity.Rotate(context.Background(), mcpidentity.RotateOptions{Now: true})
	if err != nil {
		t.Fatal(err)
	}
	newer := proxied("after the rotation", mineCred, "", &owner, false, "none")
	if kidOf(t, newer.raw) != now.Kid {
		t.Errorf("signed with %s, want %s", kidOf(t, newer.raw), now.Kid)
	}
	if _, _, err := ident.verify(got.raw, "https://portal.example.com", time.Now()); err != nil {
		t.Errorf("the previous key's token no longer verifies during the overlap: %v", err)
	}
	// A leaked key: the new one signs at once and the others leave the set.
	dropped, err := app.Identity.Rotate(context.Background(), mcpidentity.RotateOptions{Now: true, DropPrevious: true})
	if err != nil {
		t.Fatal(err)
	}
	if kids := jwksKids(t, ident); len(kids) != 1 || !kids[dropped.Kid] {
		t.Fatalf("after dropping: %v", kids)
	}
	if _, _, err := ident.verify(newer.raw, "https://portal.example.com", time.Now()); err == nil {
		t.Error("a dropped key's token still verifies")
	}
	if got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'platform.mcp_identity_key_rotate'`); got != "3" {
		t.Errorf("rotations audited: %s", got)
	}

	// Send identity off: no token at all.
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"sendIdentity": false}, &srv, ifMatch(srv.Revision))
	mustCode(t, "turn the token off", code, ec, 200, "")
	if code, _ := identityCall(t, app.GatewayURL, srv.Id, mineCred, ""); code != 200 || ident.last(t, "tools/call").raw != "" {
		t.Fatal("a token with Send identity off")
	}
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"sendIdentity": true, "identityAudience": "",
		"identityEmail": false}, &srv, ifMatch(srv.Revision))
	mustCode(t, "back to the defaults", code, ec, 200, "")
	ident.setAudience(ident.url())

	// A team's own server (proxy only) gets the token too, configured by the team.
	var own apitypes.McpServer
	code, ec = e.owner.call("POST", toolsTeam+"/mcp-servers/own", map[string]any{"name": "Team ident", "slug": "team_ident", "url": ident.url(),
		"identityEmail": true}, &own, nil)
	mustCode(t, "a team's own server", code, ec, 201, "")
	if !own.SendIdentity || !own.IdentityEmail {
		t.Fatalf("team server %+v", own)
	}
	var teamTpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "team-ident", "name": "Team ident", "instructions": "Use tools.",
		"modelId": model.Id, "mcp": []any{map[string]any{"serverId": own.Id}}}, &teamTpl, nil)
	mustCode(t, "a team template with it", code, ec, 201, "")
	var teamOwn apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Team own bot", "templateId": teamTpl.Id}, &teamOwn, nil)
	mustCode(t, "deploy it", code, ec, 201, "")
	code, ec = e.member.call("POST", "/v1/agents/"+teamOwn.Id.String()+"/threads/o1/messages", map[string]any{"text": "hello", "visibility": "private"}, nil, nil)
	mustCode(t, "blair's private thread", code, ec, 202, "")
	waitReply(t, e.member, "/v1/agents/"+teamOwn.Id.String()+"/threads/o1", 2)
	if code, out := identityCall(t, app.GatewayURL, own.Id, agentCredential(t, e, "blair@localhost", teamOwn.Id), "o1"); code != 200 {
		t.Fatalf("team server call: %d %s", code, out)
	}
	got = ident.last(t, "tools/call")
	wantPerson(t, "a team server", got, &blair, true, "private")
	if got.claims.Person.Email != "blair@localhost" || got.claims.Team.Slug != "tools" {
		t.Errorf("team server claims %+v", got.claims)
	}

	// The person must still be on the team: blair leaves, her private thread names nobody.
	code, ec = e.owner.call("DELETE", toolsTeam+"/members/"+e.userID("blair@localhost").String(), nil, nil, nil)
	mustCode(t, "remove blair", code, ec, 200, "")
	proxied("a former member's private thread", teamCred, "p1", nil, false, "private")

	// Direct mode: the runtime calls the server itself, with no token.
	code, ec = e.admin.call("PATCH", "/v1/admin/mcp-servers/"+srv.Id.String(), map[string]any{"accessMode": "direct"}, &srv, ifMatch(srv.Revision))
	mustCode(t, "switch to direct", code, ec, 200, "")
	code, ec = e.owner.call("POST", ws+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate, to start with the new servers", code, ec, 200, "")
	before := len(ident.all())
	code, ec = e.owner.call("POST", ws+"/threads/w2/messages", map[string]any{"text": "use mcp__ident__whoami"}, nil, nil)
	mustCode(t, "a call in direct mode", code, ec, 202, "")
	if v := waitReply(t, e.owner, ws+"/threads/w2", 4); !strings.Contains(text(v.Messages[len(v.Messages)-1]), "you are nobody") {
		t.Fatalf("direct reply %+v", v.Messages)
	}
	direct := ident.all()[before:]
	if len(direct) == 0 {
		t.Fatal("the direct call didn't reach the server")
	}
	for _, r := range direct {
		if r.raw != "" {
			t.Errorf("direct mode %s carried a token", r.method)
		}
	}

	// Nothing secret in the logs: tokens, the server's header.
	out := logs.String()
	if !strings.Contains(out, "agent runtime started") || !strings.Contains(out, `"msg":"runtime: `) {
		t.Fatal("the logs weren't captured: nothing to check against")
	}
	for _, r := range ident.all() {
		if r.raw != "" && strings.Contains(out, r.raw) {
			t.Fatal("an identity token reached the logs")
		}
	}
	if strings.Contains(out, firstToken[strings.LastIndexByte(firstToken, '.')+1:]) || strings.Contains(out, "ident-secret") {
		t.Fatal("a token's signature or the server's secret reached the logs")
	}
}

// grepLines are the lines of s that contain sub.
func grepLines(s, sub string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func kidOf(t *testing.T, raw string) string {
	t.Helper()
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(tok.Headers) != 1 {
		t.Fatalf("parse: %v", err)
	}
	return tok.Headers[0].KeyID
}

func jwksKids(t *testing.T, f *identityMCP) map[string]bool {
	t.Helper()
	set, err := f.keys()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, k := range set.Keys {
		if k.Algorithm != "ES256" || k.Use != "sig" || !k.IsPublic() {
			t.Errorf("published key %+v", k)
		}
		out[k.KeyID] = true
	}
	return out
}
