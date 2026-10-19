package doctor

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

func connectCheck(rep Report, group string) (Check, bool) {
	for _, c := range rep.Checks {
		if c.Group == group && c.Name == "public-connect" {
			return c, true
		}
	}
	return Check{}, false
}

func devCfg(dbURL string) config.Config {
	c := config.Defaults()
	c.DatabaseURL, c.AppURL, c.DevAuth = dbURL, "http://localhost:8098", true
	c.EncryptionKey = "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	c.APIKeyPepper = c.EncryptionKey
	c.Agents.DatabaseURL = dbURL
	return c
}

// The platform database is where the advisory locks live: doctor warns, with
// the SQL, while PUBLIC may connect to it, and says nothing is wrong once the
// privilege is revoked. It warns and never fails.
func TestPublicConnectToThePlatformDatabase(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	u, _ := url.Parse(dbURL)
	db := strings.TrimPrefix(u.Path, "/")
	cfg := devCfg(dbURL)

	rep := Run(ctx, cfg, Options{})
	c, ok := connectCheck(rep, "postgres")
	var role string
	if err := pool.QueryRow(ctx, "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	wantFix := fmt.Sprintf(`REVOKE CONNECT ON DATABASE "%s" FROM PUBLIC; GRANT CONNECT ON DATABASE "%s" TO "%s";`, db, db, role)
	if !ok || c.Status != Warn || !rep.OK || !strings.Contains(c.Fix, wantFix) || !strings.Contains(c.Fix, "pg_hba.conf") || !strings.Contains(c.Detail, "advisory locks") {
		t.Fatalf("PUBLIC may connect: %+v (report ok %v), want fix %q", c, rep.OK, wantFix)
	}
	t.Logf("sample:\n%s", sampleText(rep))

	// The suggested statements, run as the platform's role, silence it.
	if _, err := pool.Exec(ctx, fmt.Sprintf(`REVOKE CONNECT ON DATABASE "%s" FROM PUBLIC; GRANT CONNECT ON DATABASE "%s" TO "%s"`, db, db, role)); err != nil {
		t.Fatal(err)
	}
	rep = Run(ctx, cfg, Options{})
	if c, ok := connectCheck(rep, "postgres"); !ok || c.Status != OK || c.Fix != "" || !strings.Contains(c.Detail, "can't reach") {
		t.Fatalf("after the REVOKE: %+v", c)
	}

	// No agents database: nothing to protect from, skipped.
	cfg.Agents.DatabaseURL = ""
	if c, ok := connectCheck(Run(ctx, cfg, Options{Mode: "migrate"}), "postgres"); !ok || c.Status != Info || !strings.HasPrefix(c.Detail, "skipped") {
		t.Errorf("without an agents database: %+v", c)
	}
}

// The agents database's siblings that PUBLIC may connect to are listed, and
// one that revoked it is not.
func TestPublicConnectToSiblingDatabases(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	open, closed := "pdp_doctor_open_"+suffix, "pdp_doctor_closed_"+suffix
	for _, db := range []string{open, closed} {
		if _, err := pool.Exec(ctx, "CREATE DATABASE "+db); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), testutil.DatabaseURL())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		for _, db := range []string{open, closed} {
			_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db+" WITH (FORCE)")
		}
	})
	if _, err := pool.Exec(ctx, "REVOKE CONNECT ON DATABASE "+closed+" FROM PUBLIC"); err != nil {
		t.Fatal(err)
	}
	got, err := openDatabases(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(got, open) || slices.Contains(got, closed) {
		t.Fatalf("open databases %v: want %s and not %s", got, open, closed)
	}
	u, _ := url.Parse(dbURL)
	if self := strings.TrimPrefix(u.Path, "/"); slices.Contains(got, self) {
		t.Errorf("the agents database %s lists itself", self)
	}
	for _, template := range []string{"template0", "template1"} {
		if slices.Contains(got, template) {
			t.Errorf("%s is listed", template)
		}
	}
	// The check warns with the SQL for the first databases (this server has others).
	rep := Run(ctx, devCfg(dbURL), Options{})
	c, ok := connectCheck(rep, "agents")
	if !ok || c.Status != Warn || !rep.OK || !strings.Contains(c.Fix, "REVOKE CONNECT ON DATABASE") || !strings.Contains(c.Fix, "pg_hba.conf") {
		t.Fatalf("siblings: %+v", c)
	}
}

func sampleText(rep Report) string {
	var b strings.Builder
	_ = rep.WriteText(&b)
	return b.String()
}
