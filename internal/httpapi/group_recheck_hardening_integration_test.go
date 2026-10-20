package httpapi_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// logBuf collects what the app logs, to check what it says and doesn't.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// groupAppLogged is groupApp with its logs captured.
func groupAppLogged(t *testing.T, mutate func(*config.Config)) (*testApp, *testutil.OIDCProvider, *logBuf) {
	t.Helper()
	p := testutil.NewOIDCProvider(t)
	buf := &logBuf{}
	app := newTestAppWith(t, func(c *config.Config) {
		c.DevAuth = false
		c.OIDC.Issuer, c.OIDC.ClientID, c.OIDC.ClientSecret = p.URL, p.ClientID, p.ClientSecret
		c.OIDC.Scopes = []string{"openid", "profile", "email", "offline_access"}
		c.OIDC.AllowedGroups = []string{allowedGroup}
		c.OIDC.BootstrapAdminSubject = "boss-sub"
		c.OIDC.GroupRecheckInterval, c.OIDC.GroupRecheckGrace = 15*time.Minute, 24*time.Hour
		if mutate != nil {
			mutate(c)
		}
	}, slog.New(slog.NewTextHandler(buf, nil)), nil, false)
	return app, p, buf
}

// A stored refresh token the platform can't open (ENCRYPTION_KEY changed
// without ENCRYPTION_KEY_PREVIOUS) is verdict unknown, not "nothing to say":
// the person keeps access for the grace period and then loses it, the log says
// why (naming the person by ID, never a token), and the check isn't skipped
// for good.
func TestGroupRecheckUnreadableTokenEndsAccessAfterGrace(t *testing.T) {
	app, p, logs := groupAppLogged(t, nil)
	mustLogin(t, app, p, personClaims("boss-sub"))
	lena := mustLogin(t, app, p, personClaims("lena", allowedGroup))
	lenaID := userID(t, app, "lena@example.com")
	// A token sealed with a key that is no longer configured.
	exec(t, app, `UPDATE user_idp_tokens SET refresh_token_enc = '\x00' WHERE user_id = $1`, lenaID)

	dueNow(t, app)
	if n := recheck(t, app); n != 1 {
		t.Fatalf("checked %d", n)
	}
	if code, _ := lena.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "lena@example.com") != "" {
		t.Fatalf("within the grace period: %d %q", code, denial(t, app, "lena@example.com"))
	}
	out := logs.String()
	if !strings.Contains(out, "could not be opened") || !strings.Contains(out, lenaID) || !strings.Contains(out, "ENCRYPTION_KEY_PREVIOUS") {
		t.Fatalf("the log does not say what is wrong, for whom and what to do:\n%s", out)
	}
	if p.Refreshes("lena") != 0 {
		t.Fatal("the IdP was asked with a token that could not be opened")
	}

	// Past the grace period since the IdP last vouched for them, access ends.
	exec(t, app, `UPDATE user_idp_tokens SET verified_at = now() - interval '25 hours' WHERE user_id = $1`, lenaID)
	dueNow(t, app)
	recheck(t, app)
	if code, _ := lena.raw("GET", "/v1/me", nil, nil); code != 401 || denial(t, app, "lena@example.com") != "idp_unreachable" {
		t.Fatalf("after the grace period: %d %q", code, denial(t, app, "lena@example.com"))
	}
}

func storedToken(t *testing.T, app *testApp, email string) string {
	t.Helper()
	return scalar(t, app, `SELECT encode(refresh_token_enc, 'hex') FROM user_idp_tokens WHERE user_id = (SELECT id FROM users WHERE email = $1)`, email)
}

// An IdP that rotates refresh tokens (each is good once) has already moved on
// when a check can't be finished: the new token must still be kept, or the
// next check is refused (invalid_grant) and a legitimate person is denied.
func TestGroupRecheckKeepsRotatedTokenWhenIdPCantSay(t *testing.T) {
	app, p := groupApp(t, nil)
	p.RotateRefreshTokens(true)
	mia := mustLogin(t, app, p, personClaims("mia", allowedGroup))
	before := storedToken(t, app, "mia@example.com")

	// The grant is answered (and rotates the token), but the groups can't be read: no ID token, userinfo down.
	p.OmitIDTokenOnRefresh(true)
	p.FailUserinfo(true)
	dueNow(t, app)
	recheck(t, app)
	if p.Refreshes("mia") != 1 || denial(t, app, "mia@example.com") != "" {
		t.Fatalf("refreshes %d, denial %q", p.Refreshes("mia"), denial(t, app, "mia@example.com"))
	}
	if storedToken(t, app, "mia@example.com") == before {
		t.Fatal("the rotated refresh token was thrown away with the unanswered check")
	}

	// The IdP recovers: the next check uses the rotated token and passes.
	p.OmitIDTokenOnRefresh(false)
	p.FailUserinfo(false)
	dueNow(t, app)
	recheck(t, app)
	if code, _ := mia.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "mia@example.com") != "" {
		t.Fatalf("after the IdP recovered: %d %q", code, denial(t, app, "mia@example.com"))
	}
}

// The platform being told to stop (every rollout) while a check is in flight
// must not lose the token the IdP rotated: the check that began is finished
// and recorded, and the next one is not refused.
func TestGroupRecheckRecordsAnswerWhenStopped(t *testing.T) {
	app, p := groupApp(t, nil)
	p.RotateRefreshTokens(true)
	nia := mustLogin(t, app, p, personClaims("nia", allowedGroup))
	before := storedToken(t, app, "nia@example.com")

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	p.OnRefresh(func(string) { stop() }) // SIGTERM, just after the IdP answered
	dueNow(t, app)
	if _, err := app.Auth.RecheckGroups(ctx); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	p.OnRefresh(nil)
	if p.Refreshes("nia") != 1 {
		t.Fatalf("refreshes %d", p.Refreshes("nia"))
	}
	if storedToken(t, app, "nia@example.com") == before {
		t.Fatal("the token the IdP rotated was not recorded when the platform was stopped")
	}
	dueNow(t, app)
	recheck(t, app)
	if code, _ := nia.raw("GET", "/v1/me", nil, nil); code != 200 || denial(t, app, "nia@example.com") != "" {
		t.Fatalf("after the restart: %d %q", code, denial(t, app, "nia@example.com"))
	}

	// A stop before a check begins asks nobody (and loses nothing).
	dueNow(t, app)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = app.Auth.RecheckGroups(stopped)
	if p.Refreshes("nia") != 2 {
		t.Fatalf("a stopped platform asked the IdP: refreshes %d", p.Refreshes("nia"))
	}
}

// A sign-in under the group rule that brings no refresh token (no
// offline_access, or a provider that issues none) is fail-open but loud: an
// Info log naming the person by ID only, and a count.
func TestGroupSignInWithoutRefreshTokenIsLoud(t *testing.T) {
	app, p, logs := groupAppLogged(t, func(c *config.Config) { c.OIDC.Scopes = []string{"openid", "profile", "email"} })
	pat := mustLogin(t, app, p, personClaims("pat", allowedGroup))
	mustLogin(t, app, p, personClaims("boss-sub")) // the bootstrap admin is exempt: not counted
	patID := userID(t, app, "pat@example.com")

	line := ""
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "brought no refresh token") {
			line += l + "\n"
		}
	}
	if strings.Count(line, "\n") != 1 || !strings.Contains(line, "level=INFO") || !strings.Contains(line, "user="+patID) ||
		!strings.Contains(line, "offline_access") {
		t.Fatalf("the sign-in without a token is not logged once, at Info, with the user id and the cause:\n%s", line)
	}
	for _, leak := range []string{"pat@example.com", "Pat", "rt-"} {
		if strings.Contains(line, leak) {
			t.Fatalf("the log line names %q: %s", leak, line)
		}
	}
	if code, _ := pat.raw("GET", "/v1/me", nil, nil); code != 200 {
		t.Fatalf("fail-open: the person without a token is let in: %d", code)
	}
	s := metricstest.Gather(t, app.Metrics)
	if s.Sum("pdp_group_recheck_total", "result", "no_token") != 1 {
		t.Fatalf("no_token not counted once:\n%s", s.Text)
	}
}

// The re-check counts what it did, by result, and the people it can and can't
// check as a gauge (no per-person labels).
func TestGroupRecheckMetrics(t *testing.T) {
	app, p := groupApp(t, nil)
	mustLogin(t, app, p, personClaims("quin", allowedGroup))
	mustLogin(t, app, p, personClaims("rae", allowedGroup))
	mustLogin(t, app, p, personClaims("sol", allowedGroup))
	mustLogin(t, app, p, personClaims("tess", allowedGroup))
	mustLogin(t, app, p, personClaims("uma", allowedGroup))
	count := func(result string) float64 {
		return metricstest.Gather(t, app.Metrics).Sum("pdp_group_recheck_total", "result", result)
	}

	// allowed (quin, sol, tess, uma), then denied (rae removed), unknown (the IdP is away), error (an unreadable token).
	p.UpdateClaims("rae", personClaims("rae", "staff"))
	exec(t, app, `UPDATE user_idp_tokens SET refresh_token_enc = '\x00' WHERE user_id = (SELECT id FROM users WHERE email = 'uma@example.com')`)
	dueNow(t, app)
	recheck(t, app)
	if count("allowed") != 3 || count("denied") != 1 || count("error") != 1 || count("unknown") != 0 {
		t.Fatalf("allowed %v denied %v unknown %v error %v", count("allowed"), count("denied"), count("unknown"), count("error"))
	}
	p.SetDown(true)
	dueNow(t, app)
	recheck(t, app)
	if count("unknown") != 3 || count("error") != 2 {
		t.Fatalf("unknown %v error %v", count("unknown"), count("error"))
	}
	p.SetDown(false)

	// People without a token (here: sol signed in before the re-check existed): the gauge says how many.
	exec(t, app, `DELETE FROM user_idp_tokens WHERE user_id = (SELECT id FROM users WHERE email = 'sol@example.com')`)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.Auth.RunGroupRecheck(ctx) }()
	var s metricstest.Scrape
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if s = metricstest.Gather(t, app.Metrics); s.Sum("pdp_group_recheck_people", "state", "without_token") > 0 {
			break
		}
	}
	stop()
	<-done
	// quin, tess and uma have tokens, sol has none; rae was denied and the bootstrap admin is not counted.
	if s.Sum("pdp_group_recheck_people", "state", "with_token") != 3 || s.Sum("pdp_group_recheck_people", "state", "without_token") != 1 {
		t.Fatalf("pdp_group_recheck_people:\n%s", s.Text)
	}
	for _, name := range []string{"pdp_group_recheck_total", "pdp_group_recheck_people"} {
		for _, sample := range s.Samples[name] {
			if len(sample.Labels) != 1 {
				t.Errorf("%s has labels %v", name, sample.Labels)
			}
		}
	}
	if strings.Contains(s.Text, "@example.com") {
		t.Error("a metric names a person")
	}
}
