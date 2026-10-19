package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/app"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Several platform replicas behind one Service: what a person does on one
// is seen by the others. The tests run two platform instances (two sets of
// services, two HTTP servers, two connection pools) in one process, on the
// databases of the first and a launcher both reach, as replicas share the
// Kubernetes API. People know the platform by APP_URL, the first
// instance's address; browsers (cookie jars) go to either.

// newReplica is a second platform instance on a's databases, with the same
// configuration and a's launcher (its runtimes), reached at its own address.
// Its background loops are the ones a process runs, except the reaper, which
// a test calls itself.
func newReplica(t *testing.T, a *testApp) (*testApp, httpapi.Deps) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, a.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	// What a load balancer does: requests reach the replica under the public host name.
	publicHost := strings.TrimPrefix(a.URL, "http://")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = publicHost
		handler.ServeHTTP(w, r)
	}))
	deps, err := app.NewDeps(a.cfg, pool, testutil.Logger(), app.WithLauncher(a.Agents.Host().Launcher()))
	if err != nil {
		t.Fatal(err)
	}
	if err := deps.Agents.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	deps.Health = httpapi.NewHealth(map[string]func(context.Context) error{"postgres": pool.Ping})
	deps.Web, deps.WebBuilt = testWeb, true
	handler = httpapi.NewAPIHandler(deps)
	loops, stopLoops := context.WithCancel(ctx)
	go deps.Agents.RunThreadEvents(loops, testutil.Logger())
	t.Cleanup(func() {
		stopLoops()
		srv.Close()
		deps.Agents.Close() // the runtimes belong to the first instance's cleanup
		pool.Close()
	})
	return &testApp{t: t, URL: srv.URL, Origin: a.URL, GatewayURL: a.GatewayURL, Pool: pool, Client: &http.Client{Jar: a.Client.Jar},
		Agents: deps.Agents, Models: deps.Models, MCP: deps.MCP, Auth: deps.Auth, cfg: a.cfg}, deps
}

// on is the same browser session talking to another replica.
func (s *session) on(b *testApp) *session {
	return &session{t: s.t, app: b, client: s.client, csrf: s.csrf, me: s.me}
}

// watchThreads follows the thread event stream (GET /v1/agents/events) of
// the replica s talks to.
func watchThreads(t *testing.T, s *session) <-chan apitypes.AgentThreadEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", s.app.URL+"/v1/agents/events", nil)
	res, err := s.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, res)
	}
	events := make(chan apitypes.AgentThreadEvent, 64)
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok && event == "thread" {
				var ev apitypes.AgentThreadEvent
				if json.Unmarshal([]byte(v), &ev) == nil {
					events <- ev
				}
			}
		}
	}()
	return events
}

// expectThreadEvent waits for an event about the thread that satisfies ok.
func expectThreadEvent(t *testing.T, events <-chan apitypes.AgentThreadEvent, what, threadID string, ok func(apitypes.AgentThread) bool) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.ThreadId == threadID && ev.Thread != nil && ok(*ev.Thread) {
				return
			}
		case <-deadline:
			t.Fatalf("%s: no such thread event", what)
		}
	}
}

// openStreams asks a runtime how many event streams it serves, as the reaper does.
func openStreams(t *testing.T, a *testApp, agentID string) int {
	t.Helper()
	ep, ok := a.Agents.Host().Launcher().Running()[agentID]
	if !ok {
		t.Fatal("the agent is not running")
	}
	req, _ := http.NewRequest("GET", ep.BaseURL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct{ Data struct{ OpenStreams *int } }
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil || body.Data.OpenStreams == nil {
		t.Fatalf("runtime status: %v %+v", err, body)
	}
	return *body.Data.OpenStreams
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// The agent side of two replicas: a session, thread events and an agent's
// conversation are seen on both, and an agent whose stream is open on one is
// not hibernated by the other's idle reaper.
func TestTwoReplicasShareSessionsEventsAndAgents(t *testing.T) {
	a := newTestApp(t, func(c *config.Config) { c.Agents.IdleTimeout = time.Second })
	b, _ := newReplica(t, a)
	e := newTeamEnvOn(t, a) // signed in on a
	ownerB := e.owner.on(b)
	ctx := context.Background()

	// A session made on a is valid on b, and signing out on b ends it on a.
	if code, _ := ownerB.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("a session of replica A on replica B = %d", code)
	}
	other := b.signIn("casey") // made on b (the client shares the jar of the first app's client: use its own)
	if code, _ := other.on(a).raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("a session of replica B on replica A = %d", code)
	}

	var ag apitypes.Agent
	code, ec := e.owner.call("POST", toolsTeam+"/agents", map[string]any{"name": "Replicas"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	id := ag.Id.String()
	base := "/v1/agents/" + id

	// Thread events: a subscriber on b hears what happens through a, and the
	// other way round.
	eventsOnB := watchThreads(t, ownerB)
	eventsOnA := watchThreads(t, e.owner)
	code, ec = e.owner.call("POST", base+"/threads/t1/messages", map[string]any{"text": "through A", "requestId": "r1"}, nil, nil)
	mustCode(t, "send on A", code, ec, 202, "")
	expectThreadEvent(t, eventsOnB, "a reply sent through A, on B's stream", "t1", func(th apitypes.AgentThread) bool { return !th.Busy && th.LastReplyAt != nil })
	if v := waitReply(t, ownerB, base+"/threads/t1", 2); !strings.Contains(text(v.Messages[1]), "message 1 in this chat") {
		t.Fatalf("thread read on B: %+v", v.Messages)
	}
	code, ec = ownerB.call("POST", base+"/threads/t2/messages", map[string]any{"text": "through B", "requestId": "r2"}, nil, nil)
	mustCode(t, "send on B", code, ec, 202, "")
	expectThreadEvent(t, eventsOnA, "a reply sent through B, on A's stream", "t2", func(th apitypes.AgentThread) bool { return !th.Busy && th.LastReplyAt != nil })

	// A stream open on a; b's reaper finds the agent long unused, has never
	// served it a request, and still must not stop it.
	streamCtx, closeStream := context.WithCancel(ctx)
	defer closeStream()
	req, _ := http.NewRequestWithContext(streamCtx, "GET", a.URL+base+"/threads/t1/events", nil)
	res, err := e.owner.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("event stream: %v %v", err, res)
	}
	go func() { _, _ = io.Copy(io.Discard, res.Body); res.Body.Close() }()
	waitFor(t, "the runtime to count the stream", func() bool { return openStreams(t, a, id) == 1 })
	time.Sleep(2500 * time.Millisecond) // more than the idle timeout, every request long ago
	b.Agents.Host().ReapOnce(ctx)
	a.Agents.Host().ReapOnce(ctx)
	if !b.Agents.Host().Running(id) {
		t.Fatal("an agent with a stream open on another replica was hibernated")
	}
	if code, _ := ownerB.raw("GET", base+"/threads/t1", nil, nil); code != 200 {
		t.Fatalf("the agent after the reaper's passes = %d", code)
	}

	// The stream ends: after the idle timeout it is idle, and whichever
	// replica reaps next hibernates it (once).
	closeStream()
	waitFor(t, "the runtime to see the stream end", func() bool { return openStreams(t, a, id) == 0 })
	time.Sleep(1500 * time.Millisecond)
	b.Agents.Host().ReapOnce(ctx)
	if b.Agents.Host().Running(id) {
		t.Fatal("an idle agent was not hibernated")
	}
	// ...and wakes on the next request, on either replica.
	code, ec = ownerB.call("POST", base+"/threads/t1/messages", map[string]any{"text": "again"}, nil, nil)
	mustCode(t, "send after hibernation", code, ec, 202, "")
	if v := waitReply(t, e.owner, base+"/threads/t1", 4); !strings.Contains(text(v.Messages[3]), "message 2 in this chat") {
		t.Fatalf("conversation after waking: %+v", v.Messages)
	}

	// Signing out on b ends the session on a.
	if code, ec := ownerB.call("POST", "/auth/logout", nil, nil, nil); code != 200 {
		t.Fatalf("logout on B = %d %s", code, ec)
	}
	if code, _ := e.owner.raw("GET", "/v1/me", nil, nil); code != 401 {
		t.Fatalf("a session ended on B is still valid on A: %d", code)
	}
}

// An OAuth Connect authorization that starts on one replica completes on the
// other, in either order: the request travels with the browser and the code
// and its PKCE challenge are in the database.
func TestOAuthConnectAcrossReplicas(t *testing.T) {
	a := newTestApp(t, nil)
	b, _ := newReplica(t, a)
	user := a.signIn("user")

	connectAcross := func(name string, authorize, consent, exchange, use *testApp) {
		t.Helper()
		// A new app each time (a remembered consent would skip the consent step).
		regBody, _ := json.Marshal(map[string]any{"client_name": "Chat " + name, "redirect_uris": []string{"http://127.0.0.1:5555/cb"}, "token_endpoint_auth_method": "none"})
		res, err := http.Post(a.URL+"/oauth/register", "application/json", bytes.NewReader(regBody))
		if err != nil {
			t.Fatal(err)
		}
		var reg map[string]any
		_ = json.NewDecoder(res.Body).Decode(&reg)
		res.Body.Close()
		clientID, _ := reg["client_id"].(string)
		if clientID == "" {
			t.Fatalf("registration: %v", reg)
		}
		verifier, challenge := pkcePair(name[:1])
		state := "st-" + name
		q := url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5555/cb"}, "response_type": {"code"},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}.Encode()
		code, loc, body := oauthFlow{t, authorize}.authorize(user.on(authorize), q)
		if code != 302 || !strings.HasPrefix(loc, "/connect/consent?") {
			t.Fatalf("%s: authorize on %s = %d %s %s", name, authorize.URL, code, loc, body)
		}
		var out apitypes.OAuthConsentResult
		c, ec := user.on(consent).call("POST", "/v1/oauth/consent", map[string]any{"query": q, "decision": "allow"}, &out, nil)
		mustCode(t, name+": consent", c, ec, 200, "")
		u, err := url.Parse(out.RedirectUrl)
		if err != nil || u.Query().Get("state") != state || u.Query().Get("iss") != a.URL || u.Query().Get("code") == "" {
			t.Fatalf("%s: redirect %q", name, out.RedirectUrl)
		}
		c, tok := oauthFlow{t, exchange}.exchange(clientID, u.Query().Get("code"), verifier)
		if c != 200 {
			t.Fatalf("%s: exchange = %d %v", name, c, tok)
		}
		access := tok["access_token"].(string)
		if code, ec, _ := bearer(t, use, access, "GET", "/v1/agents", nil, nil); code != 200 {
			t.Fatalf("%s: the app's token on %s = %d %s", name, use.URL, code, ec)
		}
		// The refresh token rotates on a different replica from the one that issued it.
		refresh := tok["refresh_token"].(string)
		c, tok = oauthFlow{t, use}.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh}})
		if c != 200 || tok["access_token"] == "" {
			t.Fatalf("%s: refresh = %d %v", name, c, tok)
		}
	}
	// Authorization starts on A: consent and token on B; the token works on A.
	connectAcross("a-to-b", a, b, b, a)
	// Starts on B: consent on A, the code is redeemed on B, the token works on A and B.
	connectAcross("b-to-a", b, a, b, b)
}

// Sign-in with the identity provider that starts on one replica (the state,
// nonce and PKCE verifier are in the database) ends on the other, and the
// periodic group check never asks the IdP twice about one person however
// many replicas run it.
func TestOIDCSignInAndGroupRecheckAcrossReplicas(t *testing.T) {
	a, p := groupApp(t, nil)
	b, _ := newReplica(t, a)

	// The first replica starts the sign-in; the identity provider sends the
	// browser back to APP_URL, which a load balancer may answer with b.
	p.SetClaims(personClaims("boss-sub", allowedGroup))
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hop := func(from string) *http.Response {
		t.Helper()
		res, err := browser.Get(from)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}
	res := hop(a.URL + "/auth/login")
	if res.StatusCode != 302 || !strings.HasPrefix(res.Header.Get("Location"), p.URL+"/authorize") {
		t.Fatalf("login start: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res = hop(res.Header.Get("Location"))
	callback, err := url.Parse(res.Header.Get("Location"))
	if err != nil || callback.Path != "/auth/callback" {
		t.Fatalf("the provider's redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	callback.Scheme, callback.Host = "http", strings.TrimPrefix(b.URL, "http://")
	res = hop(callback.String())
	if res.StatusCode/100 != 3 {
		t.Fatalf("callback on the other replica = %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	for _, app := range []*testApp{a, b} {
		req, _ := http.NewRequest("GET", app.URL+"/v1/me", nil)
		out, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		if out.StatusCode != 200 {
			t.Fatalf("/v1/me on %s after the sign-in = %d", app.URL, out.StatusCode)
		}
	}

	// Everyone signed in is due a check; both replicas' loops run at once.
	for _, sub := range []string{"ann", "ben", "cy", "di"} {
		mustLogin(t, a, p, personClaims(sub, allowedGroup))
	}
	dueNow(t, a)
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, app := range []*testApp{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := app.Auth.RecheckGroups(context.Background())
			if err != nil {
				t.Error(err)
			}
			counts[i] = n
		}()
	}
	wg.Wait()
	if counts[0]+counts[1] != 4 {
		t.Fatalf("people checked: %v, want 4 in all (one check each)", counts)
	}
	for _, sub := range []string{"ann", "ben", "cy", "di"} {
		if n := p.Refreshes(sub); n != 1 {
			t.Errorf("the IdP was asked %d times about %s", n, sub)
		}
	}
}

// The periodic clean-ups delete rows, so two replicas running them at once
// is harmless: every expired row goes exactly once and nothing fails.
func TestCleanupOnTwoReplicas(t *testing.T) {
	a := newTestApp(t, nil)
	b, _ := newReplica(t, a)
	ctx := context.Background()
	for i := range 40 {
		a.signIn([]string{"user", "alex", "blair", "casey"}[i%4])
	}
	exec(t, a, `UPDATE sessions SET expires_at = now() - interval '1 hour'`)
	var wg sync.WaitGroup
	deleted := make([]int64, 4)
	for i, app := range []*testApp{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, _, err := app.Auth.Cleanup(ctx)
			if err != nil {
				t.Error(err)
			}
			if _, err := app.Agents.Cleanup(ctx); err != nil {
				t.Error(err)
			}
			deleted[i] = n
		}()
	}
	wg.Wait()
	if total := deleted[0] + deleted[1] + deleted[2] + deleted[3]; total != 40 {
		t.Fatalf("expired sessions deleted: %v, want 40 in all", deleted)
	}
	if n := scalar(t, a, `SELECT count(*)::text FROM sessions`); n != "0" {
		t.Fatalf("%s sessions left", n)
	}
}
