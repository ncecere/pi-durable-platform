package doctor_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/doctor"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// With OIDC_ALLOWED_GROUPS and the re-check on, doctor counts the recently
// active people with and without a stored refresh token: a warning (with the
// cause and the fix) while some have none, info otherwise. It says nothing
// when there is no group rule.
func TestDoctorGroupRecheckPeople(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	cfg := devConfig(dbURL)
	cfg.DevAuth = false
	cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.ClientSecret = "https://idp.example.com", "pdp", "secret"
	cfg.OIDC.BootstrapAdminSubject = "boss"
	run := func(mutate func(*config.Config)) (doctor.Check, bool) {
		c := cfg
		if mutate != nil {
			mutate(&c)
		}
		return find(doctor.Run(ctx, c, doctor.Options{Timeout: 10 * time.Second}), "auth", "group re-check")
	}

	// No group rule: nothing to report.
	if _, ok := run(nil); ok {
		t.Fatal("a group re-check line without OIDC_ALLOWED_GROUPS")
	}
	cfg.OIDC.AllowedGroups = []string{"pdp-users"}
	cfg.OIDC.GroupRecheckInterval = 15 * time.Minute

	// Rule on, nobody yet.
	if c, ok := run(nil); !ok || c.Status != doctor.Info || !strings.Contains(c.Detail, "no recently active people") {
		t.Fatalf("nobody yet: %+v", c)
	}

	key, err := secrets.ParseKey(cfg.EncryptionKey)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	keep := func(subject string) { // a refresh token sealed as the platform seals it
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE oidc_subject = $1`, subject).Scan(&id); err != nil {
			t.Fatal(err)
		}
		enc, err := box.Seal([]byte("refresh-token"), []byte("idp_refresh_token:"+id.String()))
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO user_idp_tokens (user_id, refresh_token_enc) VALUES ($1, $2)`, id, enc)
	}
	person := func(subject, issuer string, lastLogin string, token bool) {
		exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, last_login_at) VALUES ($1, $2, $2 || '@example.com', now() - $3::interval)`, issuer, subject, lastLogin)
		if token {
			keep(subject)
		}
	}
	idp := "https://idp.example.com"
	person("ann", idp, "1 hour", true)
	person("bob", idp, "2 days", true)
	person("cy", idp, "3 days", false)       // active, no token
	person("dormant", idp, "60 days", false) // not recently active: not counted
	person("boss", idp, "1 hour", false)     // the bootstrap admin is never re-checked: not counted
	person("persona", "dev", "1 hour", false)

	c, ok := run(nil)
	if !ok || c.Status != doctor.Warn {
		t.Fatalf("some have no token: %+v", c)
	}
	for _, want := range []string{"1 of 3 recently active people", "(2 have one)", "never re-checked", "OIDC_SCOPES does not include offline_access"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail lacks %q: %s", want, c.Detail)
		}
	}
	if !strings.Contains(c.Fix, "offline_access") || !strings.Contains(c.Fix, "sign in again") {
		t.Errorf("fix: %s", c.Fix)
	}
	for _, leak := range []string{"example.com", "cy", "ann"} {
		if strings.Contains(c.Detail+c.Fix, leak+"@") || strings.Contains(c.Detail, " "+leak+" ") {
			t.Errorf("the check names a person: %s", c.Detail)
		}
	}
	// With offline_access configured the cause is the provider's (or an earlier sign-in).
	if c, _ := run(func(c *config.Config) { c.OIDC.Scopes = []string{"openid", "email", "offline_access"} }); !strings.Contains(c.Detail, "issues no refresh token to this client") {
		t.Errorf("cause with offline_access: %s", c.Detail)
	}
	// The warning doesn't fail doctor.
	if rep := doctor.Run(ctx, cfg, doctor.Options{}); !rep.OK {
		t.Errorf("a warning failed the report: %+v", rep.Checks)
	}

	// Everyone has a token: info.
	keep("cy")
	if c, _ := run(nil); c.Status != doctor.Info || !strings.Contains(c.Detail, "all 3 recently active people") {
		t.Errorf("everyone has a token: %+v", c)
	}
	// The re-check off: said so.
	if c, _ := run(func(c *config.Config) { c.OIDC.GroupRecheckInterval = 0 }); c.Status != doctor.Info || !strings.Contains(c.Detail, "off") {
		t.Errorf("re-check off: %+v", c)
	}
}
