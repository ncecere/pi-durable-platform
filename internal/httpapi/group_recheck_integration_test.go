package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// The group re-check (internal/auth/grouprecheck.go): people removed from the
// IdP's allowed group lose their sessions, API keys and connected apps at the
// next check; the IdP being away costs nobody access until the grace period
// ends.

const allowedGroup = "pdp-users"

func groupApp(t *testing.T, mutate func(*config.Config)) (*testApp, *testutil.OIDCProvider) {
	t.Helper()
	return oidcApp(t, func(c *config.Config) {
		c.OIDC.Scopes = []string{"openid", "profile", "email", "offline_access"}
		c.OIDC.AllowedGroups = []string{allowedGroup}
		c.OIDC.BootstrapAdminSubject = "boss-sub"
		c.OIDC.GroupRecheckInterval, c.OIDC.GroupRecheckGrace = 15*time.Minute, 24*time.Hour
		if mutate != nil {
			mutate(c)
		}
	})
}

func personClaims(sub string, groups ...string) map[string]any {
	g := make([]any, len(groups))
	for i, v := range groups {
		g[i] = v
	}
	return map[string]any{"sub": sub, "email": sub + "@example.com", "email_verified": true, "name": strings.ToUpper(sub[:1]) + sub[1:], "groups": g}
}

// oidcLogin signs a person in through the provider in a fresh browser: the
// session, and the final status (403 when sign-in is refused).
func oidcLogin(t *testing.T, app *testApp, p *testutil.OIDCProvider, claims map[string]any) (*session, int) {
	t.Helper()
	p.SetClaims(claims)
	jar, _ := cookiejar.New(nil)
	s := &session{t: t, app: app, client: &http.Client{Jar: jar}}
	res, err := s.client.Get(app.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == 200 {
		s.refresh()
	}
	return s, res.StatusCode
}

func mustLogin(t *testing.T, app *testApp, p *testutil.OIDCProvider, claims map[string]any) *session {
	t.Helper()
	s, code := oidcLogin(t, app, p, claims)
	if code != 200 {
		t.Fatalf("sign in %v: %d", claims["sub"], code)
	}
	return s
}

// dueNow makes every kept IdP token due for a check.
func dueNow(t *testing.T, app *testApp) {
	t.Helper()
	exec(t, app, `UPDATE user_idp_tokens SET checked_at = now() - interval '2 hours'`)
}

func recheck(t *testing.T, app *testApp) int {
	t.Helper()
	n, err := app.Auth.RecheckGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func userID(t *testing.T, app *testApp, email string) string {
	t.Helper()
	return scalar(t, app, `SELECT id::text FROM users WHERE email = $1`, email)
}

func denial(t *testing.T, app *testApp, email string) string {
	t.Helper()
	return scalar(t, app, `SELECT coalesce(group_denied_reason, '') FROM users WHERE email = $1`, email)
}

func auditCount(t *testing.T, app *testApp, action, target string) string {
	t.Helper()
	return scalar(t, app, `SELECT count(*)::text FROM audit_log WHERE action = $1 AND target_id = $2`, action, target)
}

type access struct {
	session *session
	key     keyClient
	app     string // an OAuth Connect access token
}

// grantAccess gives a signed-in person all three ways in: their session
// (already), a personal API key, and a connected app.
func grantAccess(t *testing.T, app *testApp, s *session) access {
	t.Helper()
	var key apitypes.ApiKeyCreated
	code, ec := s.call("POST", "/v1/me/api-keys", map[string]any{"name": "script"}, &key, nil)
	mustCode(t, "create API key", code, ec, 201, "")
	regBody, _ := json.Marshal(map[string]any{"client_name": "Chat", "redirect_uris": []string{"http://127.0.0.1/cb"}, "token_endpoint_auth_method": "none"})
	res, err := http.Post(app.URL+"/oauth/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	res.Body.Close()
	token := connect(t, oauthFlow{t, app}, s, reg["client_id"].(string), "st")
	return access{session: s, key: keyClient{t, app.URL, key.Secret}, app: token}
}

// status of each way in: session, API key, connected app.
func (a access) statuses(t *testing.T, app *testApp) [3]int {
	t.Helper()
	var out [3]int
	out[0], _ = a.session.raw("GET", "/v1/me", nil, nil)
	out[1], _ = a.key.call("GET", "/v1/agents", nil, nil)
	out[2], _, _ = bearer(t, app, a.app, "GET", "/v1/agents", nil, nil)
	return out
}

var allIn, allOut = [3]int{200, 200, 200}, [3]int{401, 401, 401}

// Removing a person from the allowed group ends their console, API key and
// connected app access within a check interval, the audit log says why, and
// signing in again once they are back restores it.
func TestGroupRecheckEndsAndRestoresAccess(t *testing.T) {
	app, p := groupApp(t, nil)
	boss := mustLogin(t, app, p, personClaims("boss-sub"))
	alice := mustLogin(t, app, p, personClaims("alice", allowedGroup, "staff"))
	aliceID := userID(t, app, "alice@example.com")
	acc := grantAccess(t, app, alice)
	if got := acc.statuses(t, app); got != allIn {
		t.Fatalf("before: %v", got)
	}
	// The IdP's refresh token is kept sealed, for people it applies to (not the bootstrap admin).
	if n := scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens`); n != "1" {
		t.Fatalf("kept tokens: %s", n)
	}
	if strings.Contains(scalar(t, app, `SELECT encode(refresh_token_enc, 'escape') FROM user_idp_tokens`), "rt-") {
		t.Fatal("the refresh token is stored in the clear")
	}
	team := scalar(t, app, `SELECT count(*)::text FROM team_members WHERE user_id = $1`, aliceID)

	// Nothing is due until an interval has passed.
	if n := recheck(t, app); n != 0 || p.Refreshes("alice") != 0 {
		t.Fatalf("checked %d before the interval", n)
	}
	// Still in the group: the IdP is asked, access stays, the stored groups and token move on.
	before := scalar(t, app, `SELECT encode(refresh_token_enc, 'hex') FROM user_idp_tokens`)
	p.UpdateClaims("alice", personClaims("alice", allowedGroup, "staff", "new-group"))
	dueNow(t, app)
	if n := recheck(t, app); n != 1 || p.Refreshes("alice") != 1 {
		t.Fatalf("checked %d, refreshes %d", n, p.Refreshes("alice"))
	}
	if got := acc.statuses(t, app); got != allIn {
		t.Fatalf("after a check that passes: %v", got)
	}
	if g := scalar(t, app, `SELECT array_to_string(groups, ',') FROM users WHERE id = $1`, aliceID); g != "new-group,pdp-users,staff" {
		t.Fatalf("groups %s", g)
	}
	if after := scalar(t, app, `SELECT encode(refresh_token_enc, 'hex') FROM user_idp_tokens`); after == before {
		t.Fatal("the rotated refresh token was not kept")
	}

	// Removed from the group at the IdP: the next check ends everything.
	p.UpdateClaims("alice", personClaims("alice", "staff"))
	dueNow(t, app)
	if n := recheck(t, app); n != 1 {
		t.Fatalf("checked %d", n)
	}
	if got := acc.statuses(t, app); got != allOut {
		t.Fatalf("after removal: session, key, app = %v", got)
	}
	if denial(t, app, "alice@example.com") != "not_in_group" {
		t.Fatal("the denial was not recorded")
	}
	if scalar(t, app, `SELECT status FROM users WHERE id = $1`, aliceID) != "active" {
		t.Fatal("the group rule changed the admin-controlled status")
	}
	if scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens`) != "0" || scalar(t, app, `SELECT count(*)::text FROM sessions WHERE user_id = $1`, aliceID) != "0" {
		t.Fatal("the IdP token or sessions remain")
	}
	if scalar(t, app, `SELECT count(*)::text FROM oauth_tokens`) != "0" ||
		scalar(t, app, `SELECT string_agg(revoked_reason, ',') FROM oauth_grants WHERE user_id = $1`, aliceID) != "access_denied" {
		t.Fatal("the connected app was not revoked")
	}
	// Their data and teams stay.
	if got := scalar(t, app, `SELECT count(*)::text FROM team_members WHERE user_id = $1`, aliceID); got != team {
		t.Fatalf("memberships %s, was %s", got, team)
	}
	// The audit log says why; admins see it on the person.
	if auditCount(t, app, "user.group_denied", aliceID) != "1" || auditCount(t, app, "oauth.revoke", scalar(t, app, `SELECT id::text FROM oauth_grants WHERE user_id = $1`, aliceID)) != "1" {
		t.Fatalf("audit: %s", auditActionsInDB(t, app.Pool))
	}
	if why := scalar(t, app, `SELECT metadata->>'reason' FROM audit_log WHERE action = 'user.group_denied'`); !strings.Contains(why, "allowed") {
		t.Fatalf("audit reason %q", why)
	}
	var detail apitypes.UserDetail
	if code := boss.get("/v1/admin/users/"+aliceID, &detail); code != 200 || detail.User.AccessDeniedAt == nil || detail.User.AccessDeniedReason == nil ||
		*detail.User.AccessDeniedReason != "not_in_group" || detail.User.Status != "active" {
		t.Fatalf("admin view %d %+v", code, detail.User)
	}
	// A denied person isn't checked again, and can't sign in while outside the group.
	dueNow(t, app)
	if recheck(t, app) != 0 {
		t.Fatal("a denied person was checked")
	}
	if _, code := oidcLogin(t, app, p, personClaims("alice", "staff")); code != 403 {
		t.Fatalf("sign-in outside the group: %d", code)
	}
	if denial(t, app, "alice@example.com") != "not_in_group" {
		t.Fatal("a refused sign-in changed the denial")
	}

	// Back in the group: signing in restores access (the key works again; the app must be connected again).
	p.UpdateClaims("alice", personClaims("alice", allowedGroup))
	alice2 := mustLogin(t, app, p, personClaims("alice", allowedGroup))
	if denial(t, app, "alice@example.com") != "" || auditCount(t, app, "user.group_restored", aliceID) != "1" {
		t.Fatal("sign-in did not restore access")
	}
	if got := (access{session: alice2, key: acc.key, app: acc.app}).statuses(t, app); got != [3]int{200, 200, 401} {
		t.Fatalf("after restoring: session, key, app = %v", got)
	}
	if scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens`) != "1" {
		t.Fatal("no refresh token kept for the new sign-in")
	}
	dueNow(t, app)
	if recheck(t, app) != 1 || denial(t, app, "alice@example.com") != "" {
		t.Fatal("checks do not continue after a restore")
	}
}

// An IdP that is away costs nobody access until the grace period ends; one
// that refuses the stored grant ends it at once; the bootstrap admin is
// exempt; userinfo stands in for an ID token that has no groups.
func TestGroupRecheckIdPAnswers(t *testing.T) {
	app, p := groupApp(t, nil)
	boss := mustLogin(t, app, p, personClaims("boss-sub"))
	bob := mustLogin(t, app, p, personClaims("bob", allowedGroup))
	carol := mustLogin(t, app, p, personClaims("carol", allowedGroup))
	dave := mustLogin(t, app, p, personClaims("dave", allowedGroup))

	// The bootstrap admin signs in without being in the group, and is never checked (no token is kept).
	if scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens t JOIN users u ON u.id = t.user_id WHERE u.email = 'boss-sub@example.com'`) != "0" {
		t.Fatal("a refresh token is kept for the bootstrap admin")
	}
	// Even a stale token row for them would be skipped.
	exec(t, app, `INSERT INTO user_idp_tokens (user_id, refresh_token_enc, checked_at, verified_at)
		SELECT id, '\x00', now() - interval '2 hours', now() - interval '3 days' FROM users WHERE email = 'boss-sub@example.com'`)

	// The IdP is down: access stays within the grace period.
	p.SetDown(true)
	dueNow(t, app)
	recheck(t, app)
	if code, _ := bob.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("bob while the IdP is down: %d", code)
	}
	if denial(t, app, "bob@example.com") != "" {
		t.Fatal("an outage denied someone within the grace period")
	}
	// ...and ends once the grace period has passed since the IdP last answered.
	exec(t, app, `UPDATE user_idp_tokens SET verified_at = now() - interval '25 hours' WHERE user_id = (SELECT id FROM users WHERE email = 'bob@example.com')`)
	dueNow(t, app)
	recheck(t, app)
	if code, _ := bob.raw("GET", "/v1/me", nil, nil); code != 401 || denial(t, app, "bob@example.com") != "idp_unreachable" {
		t.Fatalf("bob after the grace period: %d %q", code, denial(t, app, "bob@example.com"))
	}
	// Others still within the grace period keep access; the bootstrap admin was never touched.
	if code, _ := carol.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("carol: %d", code)
	}
	if code, _ := boss.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "boss-sub@example.com") != "" {
		t.Fatalf("bootstrap admin: %d", code)
	}
	p.SetDown(false)

	// The IdP refuses the grant (person disabled, grant revoked): access ends at once.
	p.Revoke("carol")
	dueNow(t, app)
	recheck(t, app)
	if code, _ := carol.raw("GET", "/v1/me", nil, nil); code != 401 || denial(t, app, "carol@example.com") != "idp_refused" {
		t.Fatalf("carol after the IdP refused: %d %q", code, denial(t, app, "carol@example.com"))
	}

	// An ID token without the groups claim: userinfo says (and a removal is seen there too).
	p.OmitIDTokenOnRefresh(true)
	dueNow(t, app)
	recheck(t, app)
	if code, _ := dave.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("dave via userinfo: %d", code)
	}
	p.UpdateClaims("dave", personClaims("dave", "staff"))
	dueNow(t, app)
	recheck(t, app)
	if code, _ := dave.raw("GET", "/v1/me", nil, nil); code != 401 || denial(t, app, "dave@example.com") != "not_in_group" {
		t.Fatalf("dave removed, seen through userinfo: %d %q", code, denial(t, app, "dave@example.com"))
	}
	// A claim the IdP doesn't give at all is not an answer: access stays within the grace period.
	erin := mustLogin(t, app, p, personClaims("erin", allowedGroup))
	p.UpdateClaims("erin", map[string]any{"sub": "erin", "email": "erin@example.com", "email_verified": true})
	dueNow(t, app)
	recheck(t, app)
	if code, _ := erin.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "erin@example.com") != "" {
		t.Fatalf("erin with no groups claim at all: %d", code)
	}
}

// Suspension is the admin's and the group rule the provider's: neither
// overwrites the other, and people nobody has seen lately aren't asked about.
func TestGroupRecheckStatusAndActivity(t *testing.T) {
	app, p := groupApp(t, nil)
	boss := mustLogin(t, app, p, personClaims("boss-sub"))
	mustLogin(t, app, p, personClaims("frank", allowedGroup))
	frankID := userID(t, app, "frank@example.com")

	// An admin suspends frank; later the provider removes him from the group: both stay recorded.
	var detail apitypes.UserDetail
	boss.get("/v1/admin/users/"+frankID, &detail)
	code, ec := boss.call("PATCH", "/v1/admin/users/"+frankID, map[string]any{"status": "suspended"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "suspend", code, ec, 200, "")
	p.UpdateClaims("frank", personClaims("frank", "staff"))
	dueNow(t, app)
	if recheck(t, app) != 1 || denial(t, app, "frank@example.com") != "not_in_group" {
		t.Fatal("a suspended person was not checked")
	}
	boss.get("/v1/admin/users/"+frankID, &detail)
	if detail.User.Status != "suspended" || detail.User.AccessDeniedAt == nil {
		t.Fatalf("suspended and denied: %+v", detail.User)
	}
	// Reactivating him doesn't undo the group rule.
	code, ec = boss.call("PATCH", "/v1/admin/users/"+frankID, map[string]any{"status": "active"}, &detail.User, ifMatch(detail.User.Revision))
	mustCode(t, "reactivate", code, ec, 200, "")
	if detail.User.Status != "active" || detail.User.AccessDeniedAt == nil {
		t.Fatalf("reactivated: %+v", detail.User)
	}
	if _, code := oidcLogin(t, app, p, personClaims("frank", "staff")); code != 403 {
		t.Fatalf("frank signs in outside the group: %d", code)
	}
	// And the group rule doesn't undo a suspension: back in the group, sign-in is still refused while suspended.
	exec(t, app, `UPDATE users SET status = 'suspended' WHERE id = $1`, frankID)
	if _, code := oidcLogin(t, app, p, personClaims("frank", allowedGroup)); code != 403 || denial(t, app, "frank@example.com") == "" {
		t.Fatalf("suspended frank back in the group: %d %q", code, denial(t, app, "frank@example.com"))
	}

	// People nobody has seen lately (no session, no key or app in use, no recent sign-in) aren't asked about.
	mustLogin(t, app, p, personClaims("gail", allowedGroup))
	p.UpdateClaims("gail", personClaims("gail", "staff"))
	exec(t, app, `DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE email = 'gail@example.com')`)
	exec(t, app, `UPDATE users SET last_login_at = now() - interval '40 days' WHERE email = 'gail@example.com'`)
	dueNow(t, app)
	if recheck(t, app) != 0 || denial(t, app, "gail@example.com") != "" {
		t.Fatal("a dormant person was checked")
	}
	// Until something shows they are back (here a key in use).
	exec(t, app, `INSERT INTO api_keys (user_id, name, prefix, digest, pepper_id, expires_at, last_used_at)
		SELECT id, 'k', 'pdp_abcdefgh', repeat('a', 64), '\x0102030405060708', now() + interval '1 day', now() FROM users WHERE email = 'gail@example.com'`)
	if recheck(t, app) != 1 || denial(t, app, "gail@example.com") != "not_in_group" {
		t.Fatal("a person using a key was not checked")
	}
}

// Without OIDC_ALLOWED_GROUPS nothing is kept or checked; a denial left by a
// removed rule is lifted.
func TestGroupRecheckOnlyWithAllowedGroups(t *testing.T) {
	app, p := groupApp(t, func(c *config.Config) { c.OIDC.AllowedGroups = nil })
	gina := mustLogin(t, app, p, personClaims("gina", "staff"))
	if scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens`) != "0" {
		t.Fatal("a refresh token is kept without a group rule")
	}
	if recheck(t, app) != 0 {
		t.Fatal("checks run without a group rule")
	}
	ginaID := userID(t, app, "gina@example.com")
	exec(t, app, `UPDATE users SET group_denied_at = now(), group_denied_reason = 'not_in_group' WHERE id = $1`, ginaID)
	exec(t, app, `DELETE FROM sessions`)
	app.Auth.RunGroupRecheck(context.Background()) // returns at once: nothing to re-check
	if denial(t, app, "gina@example.com") != "" || auditCount(t, app, "user.group_restored", ginaID) != "1" {
		t.Fatal("a denial left by a removed rule was not lifted")
	}
	_ = gina

	// The re-check can be turned off with the interval.
	app2, p2 := groupApp(t, func(c *config.Config) { c.OIDC.GroupRecheckInterval = 0 })
	mustLogin(t, app2, p2, personClaims("hal", allowedGroup))
	if scalar(t, app2, `SELECT count(*)::text FROM user_idp_tokens`) != "0" || recheck(t, app2) != 0 {
		t.Fatal("OIDC_GROUP_RECHECK_INTERVAL=0 still re-checks")
	}
	// Without offline_access the IdP issues no refresh token: nothing to check with, access is unchanged.
	app3, p3 := groupApp(t, func(c *config.Config) { c.OIDC.Scopes = []string{"openid", "profile", "email"} })
	ida := mustLogin(t, app3, p3, personClaims("ida", allowedGroup))
	if scalar(t, app3, `SELECT count(*)::text FROM user_idp_tokens`) != "0" || recheck(t, app3) != 0 {
		t.Fatal("tokens kept without offline_access")
	}
	p3.UpdateClaims("ida", personClaims("ida", "staff"))
	if code, _ := ida.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("ida: %d", code)
	}
}

// Development personas are never re-checked.
func TestGroupRecheckSkipsDevPersonas(t *testing.T) {
	app := newTestApp(t, nil)
	app.devLogin("user")
	if n, err := app.Auth.RecheckGroups(context.Background()); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
	if code, _ := app.me(); code != 200 {
		t.Fatalf("dev persona: %d", code)
	}
	if scalar(t, app, `SELECT count(*)::text FROM user_idp_tokens`) != "0" {
		t.Fatal("a token for a dev persona")
	}
}

// A person in more groups than are stored (200) is matched against all of
// them, at sign-in and at every re-check: the allowed group sorts after the
// first 200 here, and must neither refuse the sign-in nor deny the re-check.
func TestGroupRuleMatchesBeyondTheStoredBound(t *testing.T) {
	app, p := groupApp(t, nil)
	many := make([]string, 0, 301)
	for i := range 300 {
		many = append(many, fmt.Sprintf("aaa-%03d", i))
	}
	inGroup := append(slices.Clone(many), allowedGroup) // "pdp-users" sorts after every "aaa-" group
	jo := mustLogin(t, app, p, personClaims("jo", inGroup...))
	if n := scalar(t, app, `SELECT cardinality(groups)::text FROM users WHERE email = 'jo@example.com'`); n != "200" {
		t.Fatalf("stored %s groups, want the bound of 200", n)
	}
	dueNow(t, app)
	if n := recheck(t, app); n != 1 {
		t.Fatalf("checked %d", n)
	}
	if code, _ := jo.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "jo@example.com") != "" {
		t.Fatalf("a person in an allowed group beyond the 200th was denied at the re-check: %d %q", code, denial(t, app, "jo@example.com"))
	}
	// Still refused when none of the 300 is allowed.
	if _, code := oidcLogin(t, app, p, personClaims("kim", many...)); code != 403 {
		t.Fatalf("300 groups, none allowed: %d", code)
	}
	// And removal is still seen.
	p.UpdateClaims("jo", personClaims("jo", many...))
	dueNow(t, app)
	recheck(t, app)
	if denial(t, app, "jo@example.com") != "not_in_group" {
		t.Fatal("removal from the allowed group among 300 was not seen")
	}
}
