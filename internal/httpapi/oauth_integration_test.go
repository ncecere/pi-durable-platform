package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// oauthFlow drives OAuth Connect as an app would: browser redirects are
// not followed, so each hop can be checked.
type oauthFlow struct {
	t   *testing.T
	app *testApp
}

// pkcePair is a verifier and its S256 challenge.
func pkcePair(seed string) (verifier, challenge string) {
	verifier = strings.Repeat(seed, 50)[:50]
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// browser is a client with s's cookies that doesn't follow redirects (nil
// s: signed out).
func browser(s *session) *http.Client {
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if s != nil {
		c.Jar = s.client.Jar
	}
	return c
}

// authorize sends a browser to /oauth/authorize: the status, the Location
// and the body.
func (f oauthFlow) authorize(s *session, rawQuery string) (int, string, string) {
	f.t.Helper()
	res, err := browser(s).Get(f.app.URL + "/oauth/authorize?" + rawQuery)
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(body)
}

// form posts a form to a protocol endpoint and decodes the JSON answer.
func (f oauthFlow) form(path string, v url.Values) (int, map[string]any) {
	f.t.Helper()
	res, err := http.Post(f.app.URL+path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
	if err != nil {
		f.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// code reads the code from a redirect to the app, checking state and iss.
func (f oauthFlow) code(location, state string) string {
	f.t.Helper()
	u, err := url.Parse(location)
	if err != nil || !strings.HasPrefix(location, "http://127.0.0.1:5555/cb?") {
		f.t.Fatalf("redirect to %q", location)
	}
	q := u.Query()
	if q.Get("state") != state || q.Get("iss") != f.app.URL || q.Get("code") == "" || !strings.HasPrefix(q.Get("code"), "pdpac_") {
		f.t.Fatalf("redirect query %v", q)
	}
	return q.Get("code")
}

// exchange redeems a code: the status and the response.
func (f oauthFlow) exchange(clientID, code, verifier string) (int, map[string]any) {
	return f.form("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {"http://127.0.0.1:5555/cb"}, "code_verifier": {verifier}})
}

// bearer calls the API with an app's token: the status, the error code and
// the WWW-Authenticate header.
func bearer(t *testing.T, app *testApp, token, method, path string, body, out any) (int, string, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, app.URL+path, rdr)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 200 && res.StatusCode < 300 && out != nil {
		env := struct{ Data any }{Data: out}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("decode %s: %v: %s", path, err, raw)
		}
	}
	return res.StatusCode, errorCode(raw), res.Header.Get("WWW-Authenticate")
}

// OAuth Connect end to end: an app registers, a person signs in and
// consents, and the app acts as them on the agent routes (and nowhere else)
// until the connection is revoked.
func TestOAuthConnect(t *testing.T) {
	// A trusted app's metadata document, on this machine (the development
	// allowance), over TLS so it can be on the trusted list.
	var docURL string
	docSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": docURL, "client_name": "Trusted Chat",
			"redirect_uris": []string{"http://127.0.0.1/cb"}})
	}))
	t.Cleanup(docSrv.Close)
	docURL = docSrv.URL + "/client.json"
	app := newTestAppWith(t, nil, testutil.Logger(), func(d *httpapi.Deps) {
		d.OAuth.SetMetadataTLS(&tls.Config{RootCAs: docSrv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	}, true)
	f := oauthFlow{t, app}
	user, admin := app.signIn("user"), app.signIn("admin")

	// Discovery.
	var meta map[string]any
	res, err := http.Get(app.URL + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(res.Body).Decode(&meta)
	res.Body.Close()
	if meta["issuer"] != app.URL || meta["token_endpoint"] != app.URL+"/oauth/token" || meta["client_id_metadata_document_supported"] != true ||
		meta["authorization_response_iss_parameter_supported"] != true || len(meta["scopes_supported"].([]any)) != 1 {
		t.Fatalf("server metadata %v", meta)
	}
	res, _ = http.Get(app.URL + "/.well-known/oauth-protected-resource")
	_ = json.NewDecoder(res.Body).Decode(&meta)
	res.Body.Close()
	if meta["resource"] != app.URL || meta["authorization_servers"].([]any)[0] != app.URL {
		t.Fatalf("resource metadata %v", meta)
	}
	if res, _ := http.Get(app.URL + "/oauth/token"); res.StatusCode != 405 {
		t.Fatalf("GET /oauth/token = %d", res.StatusCode)
	}
	var cfg apitypes.AuthConfig
	if code := user.get("/v1/auth/config", &cfg); code != 200 || cfg.Connect == nil || cfg.Connect.Issuer != app.URL ||
		cfg.Connect.AuthorizationEndpoint != app.URL+"/oauth/authorize" {
		t.Fatalf("auth config %d %+v", code, cfg.Connect)
	}

	// The app registers (Dynamic Client Registration).
	regBody, _ := json.Marshal(map[string]any{"client_name": "Chat", "redirect_uris": []string{"http://127.0.0.1/cb"},
		"token_endpoint_auth_method": "none"})
	res, err = http.Post(app.URL+"/oauth/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	res.Body.Close()
	clientID, _ := reg["client_id"].(string)
	if res.StatusCode != 201 || !strings.HasPrefix(clientID, "pdpcl_") {
		t.Fatalf("register %d %v", res.StatusCode, reg)
	}
	verifier, challenge := pkcePair("v")
	query := func(state string, extra ...string) string {
		q := url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5555/cb"}, "response_type": {"code"},
			"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}, "scope": {"agents"}}
		for i := 0; i+1 < len(extra); i += 2 {
			q.Set(extra[i], extra[i+1])
		}
		return q.Encode()
	}

	// Signed out: to sign in, then back.
	code, loc, _ := f.authorize(nil, query("s1"))
	if code != 302 || !strings.HasPrefix(loc, "/?next=%2Foauth%2Fauthorize%3F") {
		t.Fatalf("signed out: %d %q", code, loc)
	}
	// Signed in: to the consent page with the same request.
	code, loc, _ = f.authorize(user, query("s1"))
	if code != 302 || loc != "/connect/consent?"+query("s1") {
		t.Fatalf("signed in: %d %q", code, loc)
	}
	var consent apitypes.OAuthConsent
	if code := user.get("/v1/oauth/consent?"+query("s1"), &consent); code != 200 || consent.Client.Name != "Chat" ||
		consent.Client.Kind != "registered" || consent.Remembered || consent.RedirectHost != "127.0.0.1:5555" ||
		consent.PersonalWorkspace == nil || len(consent.Scopes) != 1 || consent.Scopes[0] != "agents" {
		t.Fatalf("consent %d %+v", code, consent)
	}
	var decided apitypes.OAuthConsentResult
	c, ec := user.call("POST", "/v1/oauth/consent", map[string]any{"query": query("s1"), "decision": "deny"}, &decided, nil)
	mustCode(t, "deny", c, ec, 200, "")
	if !strings.Contains(decided.RedirectUrl, "error=access_denied") || strings.Contains(decided.RedirectUrl, "code=") {
		t.Fatalf("deny redirect %q", decided.RedirectUrl)
	}
	c, ec = user.call("POST", "/v1/oauth/consent", map[string]any{"query": query("s1"), "decision": "allow"}, &decided, nil)
	mustCode(t, "allow", c, ec, 200, "")
	codeA := f.code(decided.RedirectUrl, "s1")

	// A wrong verifier spends the code; it never works again.
	if c, out := f.exchange(clientID, codeA, strings.Repeat("w", 50)); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %v", c, out)
	}
	if c, out := f.exchange(clientID, codeA, verifier); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("spent code: %d %v", c, out)
	}

	// Connecting again skips consent (remembered).
	code, loc, _ = f.authorize(user, query("s2"))
	if code != 302 {
		t.Fatalf("remembered: %d %q", code, loc)
	}
	c, tokens := f.exchange(clientID, f.code(loc, "s2"), verifier)
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	if c != 200 || !strings.HasPrefix(access, "pdpat_") || !strings.HasPrefix(refresh, "pdprt_") || tokens["scope"] != "agents" ||
		tokens["token_type"] != "Bearer" || tokens["expires_in"] != float64(3600) {
		t.Fatalf("tokens %d %v", c, tokens)
	}

	// The app acts as the person: who they are, their teams, their agents.
	var me apitypes.Me
	if c, _, _ := bearer(t, app, access, "GET", "/v1/me", nil, &me); c != 200 || me.User.Email != "user@localhost" || me.CsrfToken != "" {
		t.Fatalf("me %d %+v", c, me)
	}
	var mine []apitypes.MyTeam
	if c, _, _ := bearer(t, app, access, "GET", "/v1/teams", nil, &mine); c != 200 || len(mine) != 1 || *mine[0].Kind != "personal" {
		t.Fatalf("teams %d %+v", c, mine)
	}
	ws := "/v1/teams/" + mine[0].Slug
	if c, _, _ := bearer(t, app, access, "GET", ws, nil, nil); c != 200 {
		t.Fatalf("team %d", c)
	}
	var made apitypes.Agent
	c, ec, _ = bearer(t, app, access, "POST", ws+"/agents", map[string]any{"name": "Chat assistant"}, &made)
	mustCode(t, "create an agent in the personal workspace", c, ec, 201, "")
	// An agent with a key in the person's own workspace is theirs (not
	// service-managed): one per purpose (agents_once_integration_test.go).
	var keyed apitypes.Agent
	c, ec, _ = bearer(t, app, access, "PUT", ws+"/agents/by-key/chat-1", map[string]any{"name": "Keyed"}, &keyed)
	mustCode(t, "ensure by key in the person's workspace", c, ec, 201, "")
	if keyed.ServiceManaged {
		t.Fatalf("keyed %+v", keyed)
	}
	var listed []apitypes.Agent
	if c, _, _ := bearer(t, app, access, "GET", "/v1/agents", nil, &listed); c != 200 || len(listed) != 2 {
		t.Fatalf("agents %d %+v", c, listed)
	}
	if kind := scalar(t, app, `SELECT actor_kind FROM audit_log WHERE action = 'agent.create' AND target_id = $1`, made.Id.String()); kind != "oauth" {
		t.Errorf("agent.create audited as %q", kind)
	}

	// Nowhere else: admin, members, keys and connected apps take sessions only.
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/admin/users"}, {"POST", ws + "/members"}, {"GET", "/v1/me/api-keys"}, {"GET", "/v1/me/oauth-grants"},
	} {
		if c, _, _ := bearer(t, app, access, r.method, r.path, map[string]any{"email": "x@example.com", "role": "member"}, nil); c != 401 {
			t.Errorf("%s %s with an app token = %d", r.method, r.path, c)
		}
	}

	// An app acting for a platform admin gets no admin powers.
	createTeam(t, admin, "shared", "alex@localhost")
	adminAccess := connect(t, f, admin, clientID, "s3")
	if c, _, _ := bearer(t, app, adminAccess, "GET", "/v1/me", nil, &me); c != 200 || me.Capabilities.PlatformAdmin {
		t.Fatalf("admin's app capabilities %+v", me.Capabilities)
	}
	if c, _, _ := bearer(t, app, adminAccess, "GET", "/v1/teams/shared", nil, nil); c != 404 {
		t.Fatalf("admin's app reads another team: %d", c)
	}
	if c := admin.get("/v1/teams/shared", nil); c != 200 {
		t.Fatalf("admin session reads the team: %d", c)
	}

	// Connected apps: the person's and, for platform staff, anyone's.
	var grants []apitypes.OAuthGrant
	if c := user.get("/v1/me/oauth-grants", &grants); c != 200 || len(grants) != 1 || grants[0].Client.Name != "Chat" || grants[0].LastUsedAt == nil {
		t.Fatalf("my grants %d %+v", c, grants)
	}
	if c := admin.get("/v1/admin/users/"+user.me.User.Id.String()+"/oauth-grants", &grants); c != 200 || len(grants) != 1 {
		t.Fatalf("admin lists grants %d %+v", c, grants)
	}

	// Refresh rotates; a second use within the grace gets a pair too.
	refreshForm := func(tok string) url.Values {
		return url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tok}}
	}
	c, rotated := f.form("/oauth/token", refreshForm(refresh))
	if c != 200 || rotated["refresh_token"] == refresh || rotated["scope"] != "agents" {
		t.Fatalf("refresh %d %v", c, rotated)
	}
	if c, again := f.form("/oauth/token", refreshForm(refresh)); c != 200 || again["access_token"] == "" {
		t.Fatalf("refresh within the grace %d %v", c, again)
	}
	if c, out := f.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {"pdpcl_other"},
		"refresh_token": {rotated["refresh_token"].(string)}}); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("another client's refresh %d %v", c, out)
	}
	newAccess := rotated["access_token"].(string)
	if c, _, _ := bearer(t, app, newAccess, "GET", "/v1/agents", nil, nil); c != 200 {
		t.Fatalf("rotated access %d", c)
	}
	// Reuse after the grace: the grant is revoked, every token with it.
	exec(t, app, `UPDATE oauth_tokens SET rotated_at = now() - interval '1 minute' WHERE rotated_at IS NOT NULL`)
	if c, out := f.form("/oauth/token", refreshForm(refresh)); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("reuse %d %v", c, out)
	}
	c, ec, challengeHdr := bearer(t, app, newAccess, "GET", "/v1/agents", nil, nil)
	mustCode(t, "token of a revoked grant", c, ec, 401, "unauthorized")
	if !strings.Contains(challengeHdr, `resource_metadata="`+app.URL+`/.well-known/oauth-protected-resource"`) ||
		!strings.Contains(challengeHdr, `error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate %q", challengeHdr)
	}
	if reason := scalar(t, app, `SELECT metadata->>'reason' FROM audit_log WHERE action = 'oauth.revoke' ORDER BY id DESC LIMIT 1`); reason != "refresh_reuse" {
		t.Fatalf("revoke reason %q", reason)
	}
	// The metrics saw the token pairs issued (the code exchange, and the
	// rotation and the repeat within the grace), the refusals and the reuse.
	sc := metricstest.Gather(t, app.Metrics)
	if sc.Sum("pdp_oauth_tokens_issued_total", "grant_type", "authorization_code") < 1 || sc.Sum("pdp_oauth_tokens_issued_total", "grant_type", "refresh_token") != 2 ||
		sc.Sum("pdp_oauth_refresh_reuse_revocations_total") != 1 || sc.Sum("pdp_oauth_token_errors_total", "error", "invalid_grant") < 2 {
		t.Fatalf("oauth metrics:\n%s", sc.Text)
	}
	// Revoked consent is forgotten: connecting again asks again.
	if code, loc, _ := f.authorize(user, query("s4")); code != 302 || !strings.HasPrefix(loc, "/connect/consent?") {
		t.Fatalf("after revocation: %d %q", code, loc)
	}

	// The revocation endpoint: an access token alone, then the whole grant.
	access, refresh = connectPair(t, f, user, clientID, "s5")
	if c, _ := f.form("/oauth/revoke", url.Values{"token": {access}, "client_id": {clientID}}); c != 200 {
		t.Fatalf("revoke access %d", c)
	}
	if c, _, _ := bearer(t, app, access, "GET", "/v1/me", nil, nil); c != 401 {
		t.Fatalf("revoked access token %d", c)
	}
	if c, _ := f.form("/oauth/revoke", url.Values{"token": {refresh}, "client_id": {clientID}}); c != 200 {
		t.Fatalf("revoke refresh %d", c)
	}
	if c, out := f.form("/oauth/token", refreshForm(refresh)); c != 400 || out["error"] != "invalid_grant" {
		t.Fatalf("revoked refresh %d %v", c, out)
	}

	// The person disconnects the app in the web app.
	access, _ = connectPair(t, f, user, clientID, "s6")
	if c := user.get("/v1/me/oauth-grants", &grants); c != 200 || len(grants) != 1 {
		t.Fatalf("grants %d %+v", c, grants)
	}
	c, ec = user.call("DELETE", "/v1/me/oauth-grants/"+grants[0].Id.String(), nil, nil, nil)
	mustCode(t, "disconnect", c, ec, 200, "")
	if c, _, _ := bearer(t, app, access, "GET", "/v1/me", nil, nil); c != 401 {
		t.Fatalf("disconnected app %d", c)
	}

	// Errors before the client and redirect URI are known good are pages,
	// never redirects; later ones go back to the app.
	for name, q := range map[string]string{
		"repeated parameter": query("s7") + "&state=again",
		"bad redirect":       strings.Replace(query("s7"), "5555%2Fcb", "5555%2Fother", 1),
		"unknown client":     strings.Replace(query("s7"), clientID, "pdpcl_nope", 1),
	} {
		code, loc, body := f.authorize(user, q)
		if code != 400 || loc != "" || !strings.Contains(body, "Can't connect this app") {
			t.Errorf("%s: %d %q", name, code, loc)
		}
	}
	for want, q := range map[string]string{
		"invalid_scope":  query("s8", "scope", "agents admin"),
		"invalid_target": query("s8", "resource", "https://elsewhere.example.com"),
		"invalid_request": url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5555/cb"},
			"response_type": {"code"}, "state": {"s8"}}.Encode(),
	} {
		code, loc, _ := f.authorize(user, q)
		u, _ := url.Parse(loc)
		if code != 302 || u.Query().Get("error") != want || u.Query().Get("state") != "s8" || u.Query().Get("iss") != app.URL {
			t.Errorf("%s: %d %q", want, code, loc)
		}
	}
	// The resource, when sent, must be the platform.
	if code, loc, _ := f.authorize(user, query("s9", "resource", app.URL)); code != 302 || strings.Contains(loc, "error=") {
		t.Errorf("resource = APP_URL: %d %q", code, loc)
	}

	// A trusted app skips the consent screen (never the sign-in).
	var trusted apitypes.TrustedClients
	if c := admin.get("/v1/admin/settings/oauth-clients", &trusted); c != 200 || len(trusted.Clients) != 0 {
		t.Fatalf("trusted list starts empty: %d %+v", c, trusted)
	}
	c, ec = admin.call("PUT", "/v1/admin/settings/oauth-clients", map[string]any{"clients": []any{map[string]any{"clientId": docURL,
		"label": "Chat"}}}, &trusted, ifMatch(trusted.Revision))
	mustCode(t, "trust an app", c, ec, 200, "")
	trustedQuery := url.Values{"client_id": {docURL}, "redirect_uri": {"http://127.0.0.1:5555/cb"}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {"t1"}}.Encode()
	if code, loc, _ := f.authorize(nil, trustedQuery); code != 302 || !strings.HasPrefix(loc, "/?next=") {
		t.Fatalf("trusted app, signed out: %d %q", code, loc)
	}
	code, loc, body := f.authorize(user, trustedQuery)
	if code != 302 {
		t.Fatalf("trusted app: %d %s", code, body)
	}
	if c, tok := f.exchange(docURL, f.code(loc, "t1"), verifier); c != 200 || tok["access_token"] == nil {
		t.Fatalf("trusted exchange %d %v", c, tok)
	}
	if how := scalar(t, app, `SELECT metadata->>'how' FROM audit_log WHERE action = 'oauth.consent' ORDER BY id DESC LIMIT 1`); how != "trusted" {
		t.Fatalf("trusted consent audited as %q", how)
	}

	// Personal API keys reach /v1/me; service account keys don't (they act
	// for no one).
	var keyOut apitypes.ApiKeyCreated
	c, ec = user.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &keyOut, nil)
	mustCode(t, "create API key", c, ec, 201, "")
	if c, _, _ := bearer(t, app, keyOut.Secret, "GET", "/v1/me", nil, &me); c != 200 || me.User.Email != "user@localhost" {
		t.Fatalf("API key on /v1/me %d", c)
	}
	alex := app.signIn("alex")
	var sa apitypes.ServiceAccount
	c, ec = alex.call("POST", "/v1/teams/shared/service-accounts", map[string]any{"name": "bot", "role": "editor"}, &sa, nil)
	mustCode(t, "create service account", c, ec, 201, "")
	var saKey apitypes.ServiceAccountKeyCreated
	c, ec = alex.call("POST", "/v1/teams/shared/service-accounts/"+sa.Id.String()+"/keys", map[string]any{"name": "k"}, &saKey, nil)
	mustCode(t, "create service account key", c, ec, 201, "")
	for _, path := range []string{"/v1/me", "/v1/teams", "/v1/teams/shared"} {
		c, ec, _ := bearer(t, app, saKey.Secret, "GET", path, nil, nil)
		mustCode(t, "service account key on "+path, c, ec, 403, "person_required")
	}

	// A suspended person's tokens stop working; signed in already, they get
	// a page.
	access, _ = connectPair(t, f, user, clientID, "s10")
	blair := app.signIn("blair")
	exec(t, app, `UPDATE users SET status = 'suspended' WHERE email = 'blair@localhost'`)
	if code, loc, body := f.authorize(blair, query("s11")); code != 400 || loc != "" || !strings.Contains(body, "access_denied") {
		t.Fatalf("suspended person authorizes: %d %q", code, loc)
	}
	var u apitypes.UserDetail
	admin.get("/v1/admin/users/"+user.me.User.Id.String(), &u)
	c, ec = admin.call("PATCH", "/v1/admin/users/"+user.me.User.Id.String(), map[string]any{"status": "suspended"}, nil, ifMatch(u.User.Revision))
	mustCode(t, "suspend", c, ec, 200, "")
	if c, _, _ := bearer(t, app, access, "GET", "/v1/me", nil, nil); c != 401 {
		t.Fatalf("suspended person's token %d", c)
	}

	// Audited: the app's actions as the person through it.
	page := auditPage(t, admin, "/v1/admin/audit", url.Values{"action": {"oauth.token_issue"}})
	if len(page.Items) == 0 || page.Items[0].ActorKind != "oauth" || page.Items[0].Via == nil || page.Items[0].Via.Kind != "oauth" ||
		page.Items[0].TargetType != "oauth_grant" || page.Items[0].TargetLabel == nil {
		t.Fatalf("token issue audit %+v", page.Items)
	}
	if kind := scalar(t, app, `SELECT actor_kind FROM audit_log WHERE action = 'oauth.client_register'`); kind != "system" {
		t.Fatalf("registration audited as %q", kind)
	}
	if n := scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = 'oauth.trusted_clients_update' AND target_type = 'oauth_settings'`); n != "1" {
		t.Fatalf("trusted list audits %s", n)
	}
}

// connect connects s to the app (consenting if asked) and returns an
// access token.
func connect(t *testing.T, f oauthFlow, s *session, clientID, state string) string {
	t.Helper()
	access, _ := connectPair(t, f, s, clientID, state)
	return access
}

// connectPair connects s to the app, allowing on the consent page if it is
// shown, and returns the access and refresh tokens.
func connectPair(t *testing.T, f oauthFlow, s *session, clientID, state string) (string, string) {
	t.Helper()
	verifier, challenge := pkcePair("c")
	q := url.Values{"client_id": {clientID}, "redirect_uri": {"http://127.0.0.1:5555/cb"}, "response_type": {"code"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "state": {state}}.Encode()
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
	return tok["access_token"].(string), tok["refresh_token"].(string)
}

func scalar(t *testing.T, app *testApp, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := app.Pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func exec(t *testing.T, app *testApp, sql string, args ...any) {
	t.Helper()
	if _, err := app.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
