package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// registerClient registers a public client (Dynamic Client Registration)
// with a loopback redirect URI and returns its client_id.
func registerClient(t *testing.T, app *testApp) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"client_name": "Chat", "redirect_uris": []string{"http://127.0.0.1/cb"}})
	res, err := http.Post(app.URL+"/oauth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	id, _ := reg["client_id"].(string)
	if res.StatusCode != 201 || id == "" {
		t.Fatalf("register %d %v", res.StatusCode, reg)
	}
	return id
}

// authQuery is an authorization request of clientID with the PKCE
// challenge of pkcePair("c"), plus extra parameters (name, value, ...).
func authQuery(clientID, state string, extra ...string) string {
	_, challenge := pkcePair("c")
	q := url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5555/cb"}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q.Encode()
}

// redirectError checks that an authorization went back to the app with an
// error (and the state and iss), and returns the error code.
func (f oauthFlow) redirectError(s *session, query, state string) string {
	f.t.Helper()
	code, loc, body := f.authorize(s, query)
	u, err := url.Parse(loc)
	if code != 302 || err != nil || !strings.HasPrefix(loc, "http://127.0.0.1:5555/cb?") {
		f.t.Fatalf("authorize: %d %q %s", code, loc, body)
	}
	q := u.Query()
	if q.Get("code") != "" || q.Get("state") != state || q.Get("iss") != f.app.URL {
		f.t.Fatalf("error redirect %v", q)
	}
	return q.Get("error")
}

// connectFull connects s to the app with extra authorization parameters,
// allowing on the consent page if it is shown, and returns the token
// response.
func connectFull(t *testing.T, f oauthFlow, s *session, clientID, state string, extra ...string) map[string]any {
	t.Helper()
	verifier, _ := pkcePair("c")
	q := authQuery(clientID, state, extra...)
	code, loc, body := f.authorize(s, q)
	if code != 302 {
		t.Fatalf("authorize %d %s", code, body)
	}
	if strings.HasPrefix(loc, "/connect/consent?") {
		var out apitypes.OAuthConsentResult
		c, ec := s.call("POST", "/v1/oauth/consent", map[string]any{"query": q, "decision": "allow"}, &out, nil)
		mustCode(t, "allow", c, ec, 200, "")
		loc = out.RedirectUrl
	}
	c, tok := f.exchange(clientID, f.code(loc, state), verifier)
	if c != 200 {
		t.Fatalf("exchange %d %v", c, tok)
	}
	return tok
}

// prompt=none never shows a page: login_required signed out (or signed in
// as another account than login_hint), consent_required without a grant
// for the scope, interaction_required when the account can't use the
// platform, and a code at once when the app is connected. A login_hint that
// isn't the signed-in person never connects them: the consent page says so
// and refuses to allow.
func TestOAuthPromptNoneAndLoginHint(t *testing.T) {
	app := newTestApp(t, nil)
	f := oauthFlow{t, app}
	clientID := registerClient(t, app)
	user := app.signIn("user")
	verifier, _ := pkcePair("c")

	if got := f.redirectError(nil, authQuery(clientID, "n1", "prompt", "none"), "n1"); got != "login_required" {
		t.Fatalf("signed out: %q", got)
	}
	if got := f.redirectError(user, authQuery(clientID, "n2", "prompt", "none"), "n2"); got != "consent_required" {
		t.Fatalf("signed in, never connected: %q", got)
	}
	// Signed out without prompt=none: to sign in (the development sign-in here), then back.
	if code, loc, _ := f.authorize(nil, authQuery(clientID, "n3", "login_hint", "user@localhost")); code != 302 ||
		!strings.HasPrefix(loc, "/?next=%2Foauth%2Fauthorize%3F") {
		t.Fatalf("signed out with a hint: %d %q", code, loc)
	}

	connectFull(t, f, user, clientID, "c1")
	consents := scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = 'oauth.consent'`)

	// Connected: silent, with or without a hint naming them (case-insensitively).
	for i, extra := range [][]string{{"prompt", "none"}, {"prompt", "none", "login_hint", "USER@localhost"}} {
		state := "s" + string(rune('a'+i))
		code, loc, body := f.authorize(user, authQuery(clientID, state, extra...))
		if code != 302 {
			t.Fatalf("silent reconnect %v: %d %s", extra, code, body)
		}
		if c, tok := f.exchange(clientID, f.code(loc, state), verifier); c != 200 || tok["access_token"] == nil || tok["subject"] == nil {
			t.Fatalf("silent exchange %d %v", c, tok)
		}
	}
	if n := scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = 'oauth.consent'`); n != consents {
		t.Fatalf("a silent reconnect recorded consent again: %s -> %s", consents, n)
	}

	// Another account than the one signed in: never silently connected.
	if got := f.redirectError(user, authQuery(clientID, "m1", "prompt", "none", "login_hint", "alex@localhost"), "m1"); got != "login_required" {
		t.Fatalf("prompt=none, another account: %q", got)
	}
	mismatch := authQuery(clientID, "m2", "login_hint", "alex@localhost")
	code, loc, _ := f.authorize(user, mismatch)
	if code != 302 || loc != "/connect/consent?"+mismatch {
		t.Fatalf("another account, interactive: %d %q", code, loc)
	}
	var consent apitypes.OAuthConsent
	if c := user.get("/v1/oauth/consent?"+mismatch, &consent); c != 200 || consent.AccountMismatch == nil || !*consent.AccountMismatch ||
		consent.LoginHint == nil || *consent.LoginHint != "alex@localhost" || !consent.Remembered {
		t.Fatalf("consent view of a mismatch: %d %+v", c, consent)
	}
	c, ec := user.call("POST", "/v1/oauth/consent", map[string]any{"query": mismatch, "decision": "allow"}, nil, nil)
	mustCode(t, "allow another account", c, ec, 409, "account_mismatch")
	var decided apitypes.OAuthConsentResult
	c, ec = user.call("POST", "/v1/oauth/consent", map[string]any{"query": mismatch, "decision": "deny"}, &decided, nil)
	mustCode(t, "cancel", c, ec, 200, "")
	if !strings.Contains(decided.RedirectUrl, "error=access_denied") {
		t.Fatalf("cancel redirect %q", decided.RedirectUrl)
	}
	// A matching hint has no mismatch.
	var matching apitypes.OAuthConsent
	if c := user.get("/v1/oauth/consent?"+authQuery(clientID, "m3", "login_hint", "user@localhost"), &matching); c != 200 || matching.AccountMismatch != nil {
		t.Fatalf("consent view of a matching hint: %d %+v", c, matching)
	}

	// prompt=consent shows the page even when remembered.
	if code, loc, _ := f.authorize(user, authQuery(clientID, "p1", "prompt", "consent")); code != 302 || !strings.HasPrefix(loc, "/connect/consent?") {
		t.Fatalf("prompt=consent: %d %q", code, loc)
	}
	// Unsupported or combined prompts go back as invalid_request.
	for i, p := range []string{"login", "none consent", "select_account"} {
		state := "b" + string(rune('a'+i))
		if got := f.redirectError(user, authQuery(clientID, state, "prompt", p), state); got != "invalid_request" {
			t.Errorf("prompt=%q: %q", p, got)
		}
	}
	if got := f.redirectError(user, authQuery(clientID, "h1", "login_hint", "a\nb"), "h1"); got != "invalid_request" {
		t.Errorf("a login_hint with a newline: %q", got)
	}

	// A person who can't use the platform: a page normally, interaction_required silently.
	blair := app.signIn("blair")
	connectFull(t, f, blair, clientID, "x1")
	exec(t, app, `UPDATE users SET status = 'suspended' WHERE email = 'blair@localhost'`)
	if got := f.redirectError(blair, authQuery(clientID, "x2", "prompt", "none"), "x2"); got != "interaction_required" {
		t.Fatalf("suspended, prompt=none: %q", got)
	}
}

// With single sign-on, an app's login_hint reaches the identity provider.
func TestOAuthLoginHintReachesTheIdentityProvider(t *testing.T) {
	app, p := oidcApp(t, nil)
	f := oauthFlow{t, app}
	clientID := registerClient(t, app)
	code, loc, _ := f.authorize(nil, authQuery(clientID, "s", "login_hint", "pat@example.com"))
	if code != 302 || !strings.HasPrefix(loc, "/auth/login?next=") || !strings.Contains(loc, "login_hint=pat%40example.com") {
		t.Fatalf("to sign in: %d %q", code, loc)
	}
	res, err := browser(nil).Get(app.URL + loc)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	idp, _ := url.Parse(res.Header.Get("Location"))
	if res.StatusCode != 302 || !strings.HasPrefix(idp.String(), p.URL) || idp.Query().Get("login_hint") != "pat@example.com" {
		t.Fatalf("to the identity provider: %d %q", res.StatusCode, idp)
	}
	// A hint that isn't one line isn't passed on.
	res, _ = browser(nil).Get(app.URL + "/auth/login?next=%2F&login_hint=" + url.QueryEscape("a\nb"))
	res.Body.Close()
	if idp, _ := url.Parse(res.Header.Get("Location")); idp.Query().Has("login_hint") {
		t.Fatalf("a bad hint went to the identity provider: %q", idp)
	}
}

// agents:read: the app reads (agents, threads, events) and changes nothing;
// asking for agents later asks the person again and widens the connection;
// a token never carries more than its request.
func TestOAuthReadOnlyScope(t *testing.T) {
	app := newTestApp(t, nil)
	f := oauthFlow{t, app}
	clientID := registerClient(t, app)
	admin, user := app.signIn("admin"), app.signIn("user")
	createTeam(t, admin, "tools", "user@localhost")

	q := authQuery(clientID, "r1", "scope", "agents:read")
	var consent apitypes.OAuthConsent
	if c := user.get("/v1/oauth/consent?"+q, &consent); c != 200 || len(consent.Scopes) != 1 || consent.Scopes[0] != "agents:read" {
		t.Fatalf("consent for agents:read: %d %+v", c, consent)
	}
	tok := connectFull(t, f, user, clientID, "r1", "scope", "agents:read")
	read := tok["access_token"].(string)
	if tok["scope"] != "agents:read" {
		t.Fatalf("token scope %v", tok["scope"])
	}
	var me apitypes.Me
	if c, _, _ := bearer(t, app, read, "GET", "/v1/me", nil, &me); c != 200 || me.ConnectedApp == nil || me.ConnectedApp.ClientId != clientID ||
		me.ConnectedApp.Subject.String() != tok["subject"] || len(me.ConnectedApp.Scopes) != 1 || me.ConnectedApp.Scopes[0] != "agents:read" {
		t.Fatalf("me with a read token: %d %+v", c, me.ConnectedApp)
	}
	for _, path := range []string{"/v1/agents", "/v1/teams/tools/agents", "/v1/agents/events?once=true", "/v1/teams/tools/usage"} {
		if c, ec, _ := bearer(t, app, read, "GET", path, nil, nil); c != 200 {
			t.Errorf("GET %s with agents:read: %d %s", path, c, ec)
		}
	}
	c, ec, challenge := bearer(t, app, read, "POST", "/v1/teams/tools/agents", map[string]any{"name": "Nope"}, nil)
	mustCode(t, "create an agent with agents:read", c, ec, 403, "insufficient_scope")
	if !strings.Contains(challenge, `error="insufficient_scope"`) {
		t.Fatalf("WWW-Authenticate %q", challenge)
	}
	var ag apitypes.Agent
	c, ec = user.call("POST", "/v1/teams/tools/agents", map[string]any{"name": "Reader's"}, &ag, nil)
	mustCode(t, "create an agent", c, ec, 201, "")
	for _, r := range []struct{ method, path string }{
		{"POST", "/v1/agents/" + ag.Id.String() + "/threads"},
		{"POST", "/v1/agents/" + ag.Id.String() + "/threads/t1/messages"},
		{"PATCH", "/v1/agents/" + ag.Id.String()},
		{"DELETE", "/v1/agents/" + ag.Id.String()},
		{"POST", "/v1/agents/" + ag.Id.String() + "/hibernate"},
	} {
		if c, ec, _ := bearer(t, app, read, r.method, r.path, map[string]any{"text": "hi"}, nil); c != 403 || ec != "insufficient_scope" {
			t.Errorf("%s %s with agents:read: %d %s", r.method, r.path, c, ec)
		}
	}
	// A refresh can't ask for more.
	if c, out := f.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID},
		"refresh_token": {tok["refresh_token"].(string)}, "scope": {"agents"}}); c != 400 || out["error"] != "invalid_scope" {
		t.Fatalf("refresh asking for agents: %d %v", c, out)
	}

	// Asking for agents: not remembered (silently: consent_required); allowed, the connection widens.
	if got := f.redirectError(user, authQuery(clientID, "r2", "prompt", "none", "scope", "agents"), "r2"); got != "consent_required" {
		t.Fatalf("widening silently: %q", got)
	}
	full := connectFull(t, f, user, clientID, "r3", "scope", "agents")
	if full["scope"] != "agents" {
		t.Fatalf("widened token scope %v", full["scope"])
	}
	if c, ec, _ := bearer(t, app, full["access_token"].(string), "POST", "/v1/agents/"+ag.Id.String()+"/threads", map[string]any{}, nil); c != 201 {
		t.Fatalf("create a thread with agents: %d %s", c, ec)
	}
	if got := scalar(t, app, `SELECT array_to_string(scopes, ' ') FROM oauth_grants WHERE client_id = $1 AND revoked_at IS NULL`, clientID); got != "agents" {
		t.Fatalf("grant scopes %q", got)
	}
	if n := scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = 'oauth.consent' AND metadata->'scopes' ? 'agents'`); n != "1" {
		t.Fatalf("widening audited %s times", n)
	}
	// The old read token stays read-only; a new request for agents:read is silent and read-only.
	if c, _, _ := bearer(t, app, read, "POST", "/v1/agents/"+ag.Id.String()+"/threads", map[string]any{}, nil); c != 403 {
		t.Fatalf("an agents:read token after widening: %d", c)
	}
	code, loc, _ := f.authorize(user, authQuery(clientID, "r4", "prompt", "none", "scope", "agents:read"))
	verifier, _ := pkcePair("c")
	if c, again := f.exchange(clientID, f.code(loc, "r4"), verifier); code != 302 || c != 200 || again["scope"] != "agents:read" {
		t.Fatalf("agents:read on a wider connection: %d %d %v", code, c, again)
	}
}
