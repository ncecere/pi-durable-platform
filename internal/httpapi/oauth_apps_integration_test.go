package httpapi_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// trustedAppEnv is an app identified by a metadata document (served over
// TLS on this machine, the development allowance), on the trusted list,
// with a secret.
type trustedAppEnv struct {
	app      *testApp
	f        oauthFlow
	clientID string
	secret   string
	admin    *session
}

func newTrustedAppEnv(t *testing.T, recheck time.Duration) *trustedAppEnv {
	t.Helper()
	var docURL string
	docSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": docURL, "client_name": "Team Chat",
			"redirect_uris": []string{"http://127.0.0.1/cb"}})
	}))
	t.Cleanup(docSrv.Close)
	docURL = docSrv.URL + "/client.json"
	app := newTestAppWith(t, nil, testutil.Logger(), func(d *httpapi.Deps) {
		d.OAuth.SetMetadataTLS(&tls.Config{RootCAs: docSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	}, false)
	if recheck > 0 {
		app.Agents.SetStreamRecheck(recheck)
	}
	e := &trustedAppEnv{app: app, f: oauthFlow{t, app}, clientID: docURL, admin: app.signIn("admin")}
	var trusted apitypes.TrustedClients
	e.admin.get("/v1/admin/settings/oauth-clients", &trusted)
	c, ec := e.admin.call("PUT", "/v1/admin/settings/oauth-clients", map[string]any{"clients": []any{map[string]any{"clientId": docURL}}},
		&trusted, ifMatch(trusted.Revision))
	mustCode(t, "trust the app", c, ec, 200, "")
	var made apitypes.AppSecretCreated
	c, ec = e.admin.call("POST", "/v1/admin/settings/oauth-clients/secrets", map[string]any{"clientId": docURL}, &made, nil)
	mustCode(t, "issue a secret", c, ec, 201, "")
	if !strings.HasPrefix(made.Secret, "pdpcs_") || !strings.HasPrefix(made.Secret, made.Prefix) || !made.Trusted {
		t.Fatalf("secret %+v", made)
	}
	e.secret = made.Secret
	return e
}

// connect connects s (the app is trusted: no consent page) and returns the
// token response.
func (e *trustedAppEnv) connect(t *testing.T, s *session, state string) map[string]any {
	t.Helper()
	return connectFull(t, e.f, s, e.clientID, state)
}

// appGet calls an app-level endpoint with a bearer value.
func appGet(t *testing.T, app *testApp, secret, path string, out any) (int, string) {
	t.Helper()
	c, ec, _ := bearer(t, app, secret, "GET", path, nil, out)
	return c, ec
}

// appStream is an open app-level event stream.
type appStream struct {
	events chan apitypes.AppThreadEvent
	done   chan struct{}
}

func openAppStream(t *testing.T, app *testApp, secret string) *appStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", app.URL+"/v1/app/events", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("open the app stream: %v %v", err, res)
	}
	s := &appStream{events: make(chan apitypes.AppThreadEvent, 64), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok || !strings.Contains(data, "threadId") {
				continue
			}
			var ev apitypes.AppThreadEvent
			if json.Unmarshal([]byte(data), &ev) == nil {
				s.events <- ev
			}
		}
	}()
	return s
}

// next is the next event of thread within d (events of other threads are
// skipped; ok false when none came).
func (s *appStream) next(thread string, d time.Duration) (apitypes.AppThreadEvent, bool) {
	deadline := time.After(d)
	for {
		select {
		case ev := <-s.events:
			if ev.ThreadId == thread {
				return ev, true
			}
		case <-deadline:
			return apitypes.AppThreadEvent{}, false
		case <-s.done:
			return apitypes.AppThreadEvent{}, false
		}
	}
}

func subjectsOf(ev apitypes.AppThreadEvent) string {
	out := make([]string, len(ev.Subjects))
	for i, s := range ev.Subjects {
		out[i] = s.String()
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func sortedJoin(ids ...string) string {
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// The app-level event stream: a trusted app follows, with its secret, the
// thread changes of everyone who connected it, each change attributed to the
// people who may see it (by the rules of their own event streams), until a
// connection is revoked or expires; its secret deleted or the app no longer
// trusted ends it.
func TestOAuthAppEventStream(t *testing.T) {
	const every = 200 * time.Millisecond
	e := newTrustedAppEnv(t, every)
	app, admin := e.app, e.admin
	ann, ben, cat := app.signIn("user"), app.signIn("alex"), app.signIn("blair")
	createTeam(t, admin, "shared", "user@localhost")
	createTeam(t, admin, "cats", "blair@localhost")
	for _, email := range []string{"alex@localhost", "blair@localhost"} {
		c, ec := ann.call("POST", "/v1/teams/shared/members", map[string]any{"email": email, "role": "member"}, nil, nil)
		mustCode(t, "add "+email, c, ec, 201, "")
	}

	// Who may call the app-level endpoints: the app's secret, nothing else.
	annTok := e.connect(t, ann, "a")
	benTok := e.connect(t, ben, "b")
	e.connect(t, admin, "z") // a platform admin through the app: no staff powers, not a member
	subAnn, subBen := annTok["subject"].(string), benTok["subject"].(string)
	if subAnn == "" || subAnn == subBen || subAnn == ann.me.User.Id.String() {
		t.Fatalf("subjects %q %q", subAnn, subBen)
	}
	var me apitypes.Me
	if c, _, _ := bearer(t, app, annTok["access_token"].(string), "GET", "/v1/me", nil, &me); c != 200 || me.ConnectedApp.Subject.String() != subAnn {
		t.Fatalf("me's subject %+v", me.ConnectedApp)
	}
	for name, cred := range map[string]string{"an access token": annTok["access_token"].(string), "a made-up secret": oauthSecretLike(),
		"nothing": ""} {
		if c, _ := appGet(t, app, cred, "/v1/app/events?once=true", nil); c != 401 {
			t.Errorf("app stream with %s: %d", name, c)
		}
	}
	if c, _ := ann.raw("GET", "/v1/app/usage", nil, nil); c != 401 {
		t.Errorf("app usage with a session: %d", c)
	}

	var shared, cats apitypes.Agent
	c, ec := ann.call("POST", "/v1/teams/shared/agents", map[string]any{"name": "Shared"}, &shared, nil)
	mustCode(t, "shared agent", c, ec, 201, "")
	c, ec = cat.call("POST", "/v1/teams/cats/agents", map[string]any{"name": "Cat's"}, &cats, nil)
	mustCode(t, "cat's agent", c, ec, 201, "")

	stream := openAppStream(t, app, e.secret)
	// newThread starts a thread as s and makes it an ordinary one (as its first message would).
	newThread := func(s *session, ag apitypes.Agent, visibility string) string {
		t.Helper()
		var th apitypes.AgentThread
		c, ec := s.call("POST", "/v1/agents/"+ag.Id.String()+"/threads", map[string]any{"visibility": visibility}, &th, nil)
		mustCode(t, "start a thread", c, ec, 201, "")
		if s != cat {
			// Its creation (reserved: the creator's alone) is announced first.
			if _, ok := stream.next(th.ThreadId, 5*time.Second); !ok {
				t.Fatal("no event for a new thread")
			}
		}
		exec(t, app, `UPDATE agent_threads SET reserved = false WHERE agent_id = $1 AND thread_id = $2`, ag.Id, th.ThreadId)
		return th.ThreadId
	}
	rename := func(s *session, ag apitypes.Agent, thread, title string) {
		t.Helper()
		c, ec := s.call("PATCH", "/v1/agents/"+ag.Id.String()+"/threads/"+thread, map[string]any{"title": title}, nil, nil)
		mustCode(t, "rename", c, ec, 200, "")
	}

	// A reserved thread is its creator's alone.
	var reserved apitypes.AgentThread
	c, ec = ann.call("POST", "/v1/agents/"+shared.Id.String()+"/threads", map[string]any{"visibility": "shared"}, &reserved, nil)
	mustCode(t, "reserve a thread", c, ec, 201, "")
	if ev, ok := stream.next(reserved.ThreadId, 5*time.Second); !ok || subjectsOf(ev) != subAnn || ev.Thread == nil || ev.AgentId != shared.Id {
		t.Fatalf("reserved thread event %+v %v", ev, ok)
	}

	// A shared thread reaches both members who connected the app, not the admin (not a member).
	sharedThread := newThread(ann, shared, "shared")
	rename(ann, shared, sharedThread, "Roadmap")
	if ev, ok := stream.next(sharedThread, 5*time.Second); !ok || subjectsOf(ev) != sortedJoin(subAnn, subBen) || ev.Thread.Title != "Roadmap" {
		t.Fatalf("shared thread event %+v %v", ev, ok)
	}
	// Ann's private thread is attributed to Ann only.
	private := newThread(ann, shared, "private")
	rename(ann, shared, private, "Salary")
	if ev, ok := stream.next(private, 5*time.Second); !ok || subjectsOf(ev) != subAnn {
		t.Fatalf("private thread event %+v %v", ev, ok)
	}
	// Cat never connected the app, and Ann and Ben aren't in Cat's team: nothing.
	catThread := newThread(cat, cats, "shared")
	rename(cat, cats, catThread, "Cat's plan")
	rename(ann, shared, sharedThread, "Marker 1")
	if ev, ok := stream.next(sharedThread, 5*time.Second); !ok || ev.Thread.Title != "Marker 1" {
		t.Fatalf("marker %+v %v", ev, ok)
	}
	if _, ok := stream.next(catThread, 300*time.Millisecond); ok {
		t.Fatal("a thread of someone who never connected the app, in a team of nobody who did, reached the app")
	}
	// Deleting Ann's private thread reaches Ann only (as on her own stream; a shared
	// thread's deletion reaches everyone who may use the agent).
	var gone apitypes.AgentThread
	c, ec = ann.call("POST", "/v1/agents/"+shared.Id.String()+"/threads", map[string]any{"visibility": "private"}, &gone, nil)
	mustCode(t, "reserve a private thread", c, ec, 201, "")
	if ev, ok := stream.next(gone.ThreadId, 5*time.Second); !ok || subjectsOf(ev) != subAnn {
		t.Fatalf("private reserved thread event %+v %v", ev, ok)
	}
	c, ec = ann.call("DELETE", "/v1/agents/"+shared.Id.String()+"/threads/"+gone.ThreadId, nil, nil, nil)
	mustCode(t, "delete the private thread", c, ec, 200, "")
	if ev, ok := stream.next(gone.ThreadId, 5*time.Second); !ok || !ev.Deleted || subjectsOf(ev) != subAnn {
		t.Fatalf("deleted thread event %+v %v", ev, ok)
	}

	// Ben disconnects the app: from the next change on, his events stop.
	var grants []apitypes.OAuthGrant
	ben.get("/v1/me/oauth-grants", &grants)
	c, ec = ben.call("DELETE", "/v1/me/oauth-grants/"+grants[0].Id.String(), nil, nil, nil)
	mustCode(t, "Ben disconnects", c, ec, 200, "")
	rename(ann, shared, sharedThread, "After Ben left")
	if ev, ok := stream.next(sharedThread, 5*time.Second); !ok || subjectsOf(ev) != subAnn {
		t.Fatalf("after a revocation %+v %v", ev, ok)
	}
	// Ann's connection expires (no refresh token left): nobody is left to tell.
	exec(t, app, `UPDATE oauth_tokens SET expires_at = now() - interval '1 second'
		WHERE grant_id IN (SELECT id FROM oauth_grants WHERE user_id = $1)`, ann.me.User.Id)
	rename(ann, shared, sharedThread, "After Ann's connection expired")
	if ev, ok := stream.next(sharedThread, 600*time.Millisecond); ok {
		t.Fatalf("an expired connection still gets events: %+v", ev)
	}

	// Deleting the app's secret ends the stream at its next check, and the secret no longer works.
	var secrets []apitypes.AppSecret
	if c := admin.get("/v1/admin/settings/oauth-clients/secrets", &secrets); c != 200 || len(secrets) != 1 || secrets[0].LastUsedAt == nil {
		t.Fatalf("secrets %d %+v", c, secrets)
	}
	c, ec = admin.call("DELETE", "/v1/admin/settings/oauth-clients/secrets/"+secrets[0].Id.String(), nil, nil, nil)
	mustCode(t, "delete the secret", c, ec, 200, "")
	select {
	case <-stream.done:
	case <-time.After(10 * every):
		t.Fatal("the stream of a deleted secret stayed open")
	}
	if c, _ := appGet(t, app, e.secret, "/v1/app/events?once=true", nil); c != 401 {
		t.Fatalf("a deleted secret: %d", c)
	}
	for _, action := range []string{"oauth.client_secret_create", "oauth.client_secret_delete"} {
		if n := scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = $1 AND target_type = 'oauth_client_secret'`, action); n != "1" {
			t.Errorf("%s audited %s times", action, n)
		}
	}
}

// oauthSecretLike is a well-formed app secret nobody issued.
func oauthSecretLike() string { return "pdpcs_" + strings.Repeat("A", 43) }

// App secrets: platform admins issue at most two per trusted app; auditors
// read them (never the secret); an app taken off the trusted list can't use
// its secrets.
func TestOAuthAppSecrets(t *testing.T) {
	e := newTrustedAppEnv(t, 0)
	app, admin, auditor := e.app, e.admin, e.app.signIn("auditor")
	c, ec := auditor.call("POST", "/v1/admin/settings/oauth-clients/secrets", map[string]any{"clientId": e.clientID}, nil, nil)
	mustCode(t, "an auditor issues a secret", c, ec, 403, "")
	c, ec = admin.call("POST", "/v1/admin/settings/oauth-clients/secrets", map[string]any{"clientId": "https://other.example.com/c.json"}, nil, nil)
	mustCode(t, "a secret for an app that isn't trusted", c, ec, 422, "client_not_trusted")
	var second apitypes.AppSecretCreated
	c, ec = admin.call("POST", "/v1/admin/settings/oauth-clients/secrets", map[string]any{"clientId": e.clientID}, &second, nil)
	mustCode(t, "a second secret", c, ec, 201, "")
	c, ec = admin.call("POST", "/v1/admin/settings/oauth-clients/secrets", map[string]any{"clientId": e.clientID}, nil, nil)
	mustCode(t, "a third secret", c, ec, 409, "too_many_secrets")
	code, raw := auditor.raw("GET", "/v1/admin/settings/oauth-clients/secrets", nil, nil)
	if code != 200 || strings.Contains(string(raw), e.secret) || strings.Contains(string(raw), second.Secret) || strings.Contains(string(raw), `"secret"`) {
		t.Fatalf("the list shows secrets: %d %s", code, raw)
	}
	if n := scalar(t, app, `SELECT count(*)::text FROM oauth_client_secrets WHERE secret_hash LIKE 'pdpcs_%'`); n != "0" {
		t.Fatal("a secret stored in clear")
	}
	for _, s := range []string{e.secret, second.Secret} {
		if c, _ := appGet(t, app, s, "/v1/app/usage", nil); c != 200 {
			t.Fatalf("a secret of a trusted app: %d", c)
		}
	}
	// Off the trusted list: every secret stops working, and the list says why.
	var trusted apitypes.TrustedClients
	admin.get("/v1/admin/settings/oauth-clients", &trusted)
	c, ec = admin.call("PUT", "/v1/admin/settings/oauth-clients", map[string]any{"clients": []any{}}, nil, ifMatch(trusted.Revision))
	mustCode(t, "untrust", c, ec, 200, "")
	if c, _ := appGet(t, app, second.Secret, "/v1/app/usage", nil); c != 401 {
		t.Fatalf("a secret of an app no longer trusted: %d", c)
	}
	var secrets []apitypes.AppSecret
	if admin.get("/v1/admin/settings/oauth-clients/secrets", &secrets); len(secrets) != 2 || secrets[0].Trusted {
		t.Fatalf("secrets after untrusting %+v", secrets)
	}
}

// Usage per connected app: the model requests an agent makes answering a
// message sent through an app's token are attributed to that connection;
// others aren't. The app reads its people's usage by person and day (paged,
// bounded); the platform's report filters by app; and work of an agent
// answering two apps at once is attributed to neither.
func TestOAuthAppUsage(t *testing.T) {
	e := newTrustedAppEnv(t, 0)
	app, admin := e.app, e.admin
	ann, ben := app.signIn("user"), app.signIn("alex")
	createTeam(t, admin, "shared", "user@localhost")
	c, ec := ann.call("POST", "/v1/teams/shared/members", map[string]any{"email": "alex@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add Ben", c, ec, 201, "")

	fake := newFakeProvider(t)
	var prov apitypes.ModelProvider
	c, ec = admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "Fake", "kind": "openai-compatible",
		"baseUrl": fake.srv.URL + "/v1/", "apiKey": fakeProviderKey}, &prov, nil)
	mustCode(t, "provider", c, ec, 201, "")
	var model apitypes.Model
	c, ec = admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models",
		map[string]any{"upstreamId": "fake-1", "name": "Fake One", "inputPrice": 1.5}, &model, nil)
	mustCode(t, "model", c, ec, 201, "")
	var ag apitypes.Agent
	c, ec = ann.call("POST", "/v1/teams/shared/agents", map[string]any{"name": "Usage", "modelId": model.Id}, &ag, nil)
	mustCode(t, "agent", c, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()

	annTok := e.connect(t, ann, "a")
	benTok := e.connect(t, ben, "b")
	// Ann through the app; then Ben in the console (not through the app).
	c, ec, _ = bearer(t, app, annTok["access_token"].(string), "POST", base+"/threads/t1/messages", map[string]any{"text": "hello"}, nil)
	mustCode(t, "Ann sends through the app", c, ec, 202, "")
	waitReply(t, ann, base+"/threads/t1", 2)
	c, ec = ben.call("POST", base+"/threads/t2/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "Ben sends in the console", c, ec, 202, "")
	waitReply(t, ben, base+"/threads/t2", 2)
	annGrant := scalar(t, app, `SELECT id::text FROM oauth_grants WHERE user_id = $1 AND revoked_at IS NULL`, ann.me.User.Id)
	benGrant := scalar(t, app, `SELECT id::text FROM oauth_grants WHERE user_id = $1 AND revoked_at IS NULL`, ben.me.User.Id)
	if got := scalar(t, app, `SELECT string_agg(coalesce(oauth_grant_id::text, '-'), ',' ORDER BY id) FROM model_usage WHERE agent_id = $1`, ag.Id); got != annGrant+",-" {
		t.Fatalf("usage attributed %q, want %q", got, annGrant+",-")
	}

	// The app reads its people's usage: Ann's request, by her subject.
	var page apitypes.AppUsagePage
	if c, ec := appGet(t, app, e.secret, "/v1/app/usage", &page); c != 200 || len(page.Rows) != 1 || page.Rows[0].Subject.String() != annTok["subject"] ||
		page.Rows[0].Requests != 1 || page.Rows[0].InputTokens != 42 || page.Rows[0].OutputTokens != 7 || page.Rows[0].CostUsd <= 0 || page.NextCursor != nil {
		t.Fatalf("app usage %d %s %+v", c, ec, page)
	}
	// More days and people, and tool calls: paged by day, then subject.
	exec(t, app, `INSERT INTO model_usage (team_id, agent_id, model_id, source, input_tokens, output_tokens, oauth_grant_id, created_at)
		SELECT agent.team_id, agent.id, agent.model_id, 'proxy', 10, 1, g.id, now() - interval '2 days'
		FROM agents agent, oauth_grants g WHERE agent.id = $1 AND g.id IN ($2, $3)`, ag.Id, annGrant, benGrant)
	exec(t, app, `INSERT INTO mcp_calls (team_id, agent_id, server_name, tool, ok, oauth_grant_id, created_at)
		SELECT team_id, id, 'search', 'find', true, $2, now() - interval '2 days' FROM agents WHERE id = $1`, ag.Id, benGrant)
	var rows []apitypes.AppUsageRow
	cursor := ""
	for i := 0; i < 5; i++ {
		path := "/v1/app/usage?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var p apitypes.AppUsagePage
		if c, ec := appGet(t, app, e.secret, path, &p); c != 200 || len(p.Rows) != 1 {
			t.Fatalf("page %d: %d %s %+v", i, c, ec, p)
		}
		rows = append(rows, p.Rows...)
		if p.NextCursor == nil {
			break
		}
		cursor = *p.NextCursor
	}
	if len(rows) != 3 || !rows[0].Day.Time.Before(rows[2].Day.Time) || rows[0].Day != rows[1].Day ||
		rows[0].Subject.String() > rows[1].Subject.String() {
		t.Fatalf("paged rows %+v", rows)
	}
	tools := int64(0)
	for _, r := range rows {
		if r.Subject.String() == benTok["subject"] {
			tools += r.ToolCalls
		}
	}
	if tools != 1 {
		t.Fatalf("Ben's tool calls %d", tools)
	}
	for _, bad := range []string{"?from=2026-02-01&to=2026-01-01", "?from=2025-01-01&to=2025-06-01", "?cursor=nope", "?limit=0", "?limit=5000"} {
		if c, _ := appGet(t, app, e.secret, "/v1/app/usage"+bad, nil); c != 400 {
			t.Errorf("app usage %s: %d", bad, c)
		}
	}

	// The platform's report filters by app; without the filter it is unchanged.
	var all, byApp apitypes.UsageReport
	admin.get("/v1/admin/usage", &all)
	if c := admin.get("/v1/admin/usage?clientId="+e.clientID, &byApp); c != 200 || byApp.Total.Requests != 3 || all.Total.Requests != 4 {
		t.Fatalf("by app %d: %d of %d requests", c, byApp.Total.Requests, all.Total.Requests)
	}
	var connected []apitypes.ConnectedApp
	if c := admin.get("/v1/admin/oauth-clients", &connected); c != 200 || len(connected) != 1 || connected[0].ClientId != e.clientID ||
		connected[0].People != 2 || !connected[0].Trusted || connected[0].Name != "Team Chat" {
		t.Fatalf("connected apps %d %+v", c, connected)
	}

	// Two apps' people replying at once on one agent: neither is attributed.
	q := dbgen.New(app.Pool)
	ctx := context.Background()
	exec(t, app, `UPDATE agent_threads SET busy = true WHERE agent_id = $1`, ag.Id)
	exec(t, app, `UPDATE agent_run_senders SET grant_id = $2, sent_at = now() WHERE agent_id = $1 AND thread_id = 't2'`, ag.Id, benGrant)
	if g := oauth.Attribution(ctx, q, ag.Id, ""); g.Valid {
		t.Fatalf("two connections at once attributed to %v", g.UUID)
	}
	if g := oauth.Attribution(ctx, q, ag.Id, "t2"); !g.Valid || g.UUID.String() != benGrant {
		t.Fatalf("one thread's work attributed to %v", g)
	}
	exec(t, app, `UPDATE agent_threads SET busy = false, last_reply_at = now() - interval '1 hour' WHERE agent_id = $1`, ag.Id)
	exec(t, app, `UPDATE agent_run_senders SET sent_at = now() - interval '1 hour' WHERE agent_id = $1`, ag.Id)
	if g := oauth.Attribution(ctx, q, ag.Id, ""); g.Valid {
		t.Fatalf("an idle agent's work attributed to %v", g.UUID)
	}
}
