package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// sender sends a request with some credential: a session (cookies and CSRF
// token) or a bearer key or token.
type sender func(*http.Request) (*http.Response, error)

func viaSession(s *session) sender {
	return func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			r.Header.Set("Origin", s.app.origin())
			r.Header.Set("X-CSRF-Token", s.csrf)
		}
		return s.client.Do(r)
	}
}

func viaBearer(token string) sender {
	return func(r *http.Request) (*http.Response, error) {
		r.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultClient.Do(r)
	}
}

// answer is a response: its status, error code, the agent it holds and
// whether it was replayed (Idempotent-Replayed).
type answer struct {
	code     int
	err      string
	agent    apitypes.Agent
	replayed bool
}

func send(t *testing.T, app *testApp, via sender, method, path string, body any, headers map[string]string) answer {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, app.URL+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := via(req)
	if err != nil {
		t.Error(err)
		return answer{}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := answer{code: res.StatusCode, err: errorCode(raw), replayed: res.Header.Get("Idempotent-Replayed") == "true"}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		env := struct{ Data *apitypes.Agent }{Data: &out.agent}
		_ = json.Unmarshal(raw, &env)
	}
	return out
}

func idem(key string) map[string]string { return map[string]string{"Idempotency-Key": key} }

// registerApp registers an OAuth Connect client (Dynamic Client
// Registration) and returns its client ID.
func registerApp(t *testing.T, app *testApp, name string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"client_name": name, "redirect_uris": []string{"http://127.0.0.1/cb"}, "token_endpoint_auth_method": "none"})
	res, err := http.Post(app.URL+"/oauth/register", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	id, _ := reg["client_id"].(string)
	if id == "" {
		t.Fatalf("register: %d %v", res.StatusCode, reg)
	}
	return id
}

// personalSlug is the person's personal workspace's slug.
func personalSlug(t *testing.T, s *session) string {
	t.Helper()
	var mine []apitypes.MyTeam
	s.get("/v1/teams", &mine)
	for _, m := range mine {
		if m.Kind != nil && *m.Kind == "personal" {
			return m.Slug
		}
	}
	t.Fatal("no personal workspace")
	return ""
}

// Agent creation with an Idempotency-Key: a retry returns the first agent,
// for people (sessions and API keys), apps acting for them and service
// accounts, each in their own scope; concurrent retries make one agent.
func TestAgentCreationIdempotency(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	admin, user := app.signIn("admin"), app.signIn("user")
	createTeam(t, admin, "tools", "user@localhost")
	ws := "/v1/teams/" + personalSlug(t, user) + "/agents"
	chat := viaBearer(createSAKey(t, user, createSA(t, user, map[string]any{"name": "chat-app", "role": "editor"}).Id.String(),
		map[string]any{"name": "k"}).Secret)
	var key apitypes.ApiKeyCreated
	code, ec := user.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &key, nil)
	mustCode(t, "API key", code, ec, 201, "")
	app1 := viaBearer(connect(t, oauthFlow{t, app}, user, registerApp(t, app, "Pocket Chat"), "s1"))
	app2 := viaBearer(connect(t, oauthFlow{t, app}, user, registerApp(t, app, "Desk Chat"), "s2"))
	me := viaSession(user)

	// A person: the first request creates, a retry replays it.
	first := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Once"}, idem("k-1"))
	if first.code != 201 || first.replayed {
		t.Fatalf("first: %+v", first)
	}
	again := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Once"}, idem("k-1"))
	if again.code != 201 || !again.replayed || again.agent.Id != first.agent.Id {
		t.Fatalf("retry: %+v", again)
	}
	// As a structured-field string, through the team's ID, and with the person's API key: the same request.
	byID := send(t, app, viaBearer(key.Secret), "POST", "/v1/teams/"+first.agent.TeamId.String()+"/agents",
		map[string]any{"name": "Once"}, idem(`"k-1"`))
	if byID.code != 201 || !byID.replayed || byID.agent.Id != first.agent.Id {
		t.Fatalf("retry with the API key: %+v", byID)
	}
	// The same key for another request: refused, whatever differs.
	for what, r := range map[string]answer{
		"another name": send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Twice"}, idem("k-1")),
		"another team": send(t, app, me, "POST", ws, map[string]any{"name": "Once"}, idem("k-1")),
	} {
		if r.code != 422 || r.err != "idempotency_key_reused" {
			t.Fatalf("%s: %+v", what, r)
		}
	}
	// Without a key, a second request creates (here: refused, the name is taken).
	if r := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Once"}, nil); r.code != 409 || r.err != "name_taken" {
		t.Fatalf("without a key: %+v", r)
	}
	for _, bad := range []string{strings.Repeat("k", 256), "k\u00e9y", `""`} {
		if r := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Bad key"}, idem(bad)); r.code != 400 || r.err != "invalid_idempotency_key" {
			t.Fatalf("key %q: %+v", bad, r)
		}
	}

	// A service account has its own scope: the person's key is free for it.
	sa := send(t, app, chat, "POST", toolsTeam+"/agents", map[string]any{"name": "Bot made"}, idem("k-1"))
	saAgain := send(t, app, chat, "POST", toolsTeam+"/agents", map[string]any{"name": "Bot made"}, idem("k-1"))
	if sa.code != 201 || sa.replayed || saAgain.code != 201 || !saAgain.replayed || saAgain.agent.Id != sa.agent.Id {
		t.Fatalf("service account: %+v then %+v", sa, saAgain)
	}

	// Each app the person connected has its own scope, apart from the person's.
	a1 := send(t, app, app1, "POST", ws, map[string]any{"name": "From an app"}, idem("k-1"))
	a1Again := send(t, app, app1, "POST", ws, map[string]any{"name": "From an app"}, idem("k-1"))
	if a1.code != 201 || a1.replayed || a1Again.code != 201 || !a1Again.replayed || a1Again.agent.Id != a1.agent.Id {
		t.Fatalf("app: %+v then %+v", a1, a1Again)
	}
	if a2 := send(t, app, app2, "POST", ws, map[string]any{"name": "From another app"}, idem("k-1")); a2.code != 201 || a2.replayed {
		t.Fatalf("another app: %+v", a2)
	}

	// Concurrent retries: one agent, every answer is it.
	answers := make([]answer, 8)
	var wg sync.WaitGroup
	for i := range answers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Racer"}, idem("k-race"))
		}()
	}
	wg.Wait()
	created := 0
	for _, r := range answers {
		if r.code != 201 || r.agent.Id != answers[0].agent.Id {
			t.Fatalf("concurrent: %+v", answers)
		}
		if !r.replayed {
			created++
		}
	}
	var n string
	if err := app.Pool.QueryRow(context.Background(), `SELECT count(*)::text FROM agents WHERE name = 'Racer'`).Scan(&n); err != nil || n != "1" || created != 1 {
		t.Fatalf("concurrent retries made %s agents (%d not replayed), %v", n, created, err)
	}

	// The agent was deleted since: the retry says so instead of creating another.
	code, ec = user.call("DELETE", "/v1/agents/"+a1.agent.Id.String(), nil, nil, nil)
	mustCode(t, "delete", code, ec, 200, "")
	if r := send(t, app, app1, "POST", ws, map[string]any{"name": "From an app"}, idem("k-1")); r.code != 404 || r.err != "agent_not_found" {
		t.Fatalf("after delete: %+v", r)
	}

	// After 24 hours a key is forgotten (and the request runs again), and the cleanup removes it.
	if _, err := app.Pool.Exec(context.Background(), `UPDATE idempotency_keys SET created_at = now() - interval '25 hours'
		WHERE caller = 'user:' || $1::text`, user.me.User.Id); err != nil {
		t.Fatal(err)
	}
	if r := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Once"}, idem("k-1")); r.code != 409 || r.err != "name_taken" {
		t.Fatalf("an expired key: %+v", r)
	}
	if n, err := app.Agents.CleanupIdempotencyKeys(context.Background()); err != nil || n != 2 {
		t.Fatalf("cleanup removed %d, %v", n, err)
	}
	if r := send(t, app, me, "POST", toolsTeam+"/agents", map[string]any{"name": "Once again"}, idem("k-1")); r.code != 201 || r.replayed {
		t.Fatalf("a forgotten key: %+v", r)
	}
}

// PUT .../agents/by-key/{key}: editor service accounts in their team
// (service-managed agents), and a person in their own personal workspace,
// however they call (a session, a personal API key, an app they connected):
// the agent is the workspace's like any other.
func TestAgentsByKeyInPersonalWorkspace(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	admin, user, alex := app.signIn("admin"), app.signIn("user"), app.signIn("alex")
	createTeam(t, admin, "tools", "user@localhost")
	ws := "/v1/teams/" + personalSlug(t, user)
	chat := viaBearer(createSAKey(t, user, createSA(t, user, map[string]any{"name": "chat-app", "role": "editor"}).Id.String(),
		map[string]any{"name": "k"}).Secret)
	var key apitypes.ApiKeyCreated
	code, ec := user.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &key, nil)
	mustCode(t, "API key", code, ec, 201, "")
	userApp := viaBearer(connect(t, oauthFlow{t, app}, user, registerApp(t, app, "Pocket Chat"), "s1"))
	alexApp := viaBearer(connect(t, oauthFlow{t, app}, alex, registerApp(t, app, "Pocket Chat"), "s2"))
	me := viaSession(user)

	// A service account, in its team: service-managed.
	sa := send(t, app, chat, "PUT", toolsTeam+"/agents/by-key/user-1", nil, nil)
	if sa.code != 201 || !sa.agent.ServiceManaged || sa.agent.ExternalKey == nil {
		t.Fatalf("service account: %+v", sa)
	}
	// A person in a shared team: refused, by any credential.
	for what, via := range map[string]sender{"session": me, "API key": viaBearer(key.Secret), "app": userApp} {
		if r := send(t, app, via, "PUT", toolsTeam+"/agents/by-key/mine", nil, nil); r.code != 403 || r.err != "forbidden" {
			t.Fatalf("a person (%s) in a shared team: %+v", what, r)
		}
	}

	// The person in their own workspace: created once, then found, by every credential of theirs.
	first := send(t, app, me, "PUT", ws+"/agents/by-key/notes", map[string]any{"name": "Notes"}, nil)
	if first.code != 201 || first.agent.ServiceManaged || first.agent.ExternalKey == nil || *first.agent.ExternalKey != "notes" ||
		first.agent.TeamSlug != personalSlug(t, user) {
		t.Fatalf("person in their workspace: %+v", first)
	}
	for what, via := range map[string]sender{"session": me, "API key": viaBearer(key.Secret), "app": userApp} {
		if r := send(t, app, via, "PUT", ws+"/agents/by-key/notes", nil, nil); r.code != 200 || r.agent.Id != first.agent.Id {
			t.Fatalf("again (%s): %+v", what, r)
		}
	}
	if r := send(t, app, userApp, "PUT", ws+"/agents/by-key/mail", nil, nil); r.code != 201 || r.agent.Id == first.agent.Id || r.agent.Name != "mail" {
		t.Fatalf("the app's second purpose: %+v", r)
	}
	// It is the person's to use, and listed like their other agents.
	base := "/v1/agents/" + first.agent.Id.String()
	code, ec = user.call("POST", base+"/threads/n1/messages", map[string]any{"text": "hello"}, nil, nil)
	mustCode(t, "the person talks to it", code, ec, 202, "")
	waitReply(t, user, base+"/threads/n1", 2)
	var listed []apitypes.Agent
	user.get("/v1/agents", &listed)
	found := 0
	for _, a := range listed {
		if a.ExternalKey != nil {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("the person's agents: %+v", listed)
	}

	// Nobody else: another person, an app acting for another person, a service account.
	if r := send(t, app, viaSession(alex), "PUT", ws+"/agents/by-key/notes", nil, nil); r.code != 404 || r.err != "team_not_found" {
		t.Fatalf("another person: %+v", r)
	}
	if r := send(t, app, alexApp, "PUT", ws+"/agents/by-key/notes", nil, nil); r.code != 404 || r.err != "team_not_found" {
		t.Fatalf("an app for another person: %+v", r)
	}
	if r := send(t, app, chat, "PUT", ws+"/agents/by-key/notes", nil, nil); r.code != 404 || r.err != "team_not_found" {
		t.Fatalf("a service account: %+v", r)
	}
	if r := send(t, app, alexApp, "GET", base, nil, nil); r.code != 404 {
		t.Fatalf("an app for another person reads it: %+v", r)
	}
}
