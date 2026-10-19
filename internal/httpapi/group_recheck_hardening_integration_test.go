package httpapi_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
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
