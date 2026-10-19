package app_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
	"github.com/ncecere/pi-durable-platform/internal/app"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// The prune-roles command's path, as an operator runs it: the platform's
// database and an agents database reached by a CREATEROLE role. A role left
// by a dropped agents database is listed by --dry-run and dropped without it.
func TestPruneRoles(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	role, password := "pdp_prune_prov_"+suffix, "prune-test-"+suffix
	u, _ := url.Parse(dbURL)
	db := strings.TrimPrefix(u.Path, "/")
	for _, stmt := range []string{
		fmt.Sprintf("CREATE ROLE %s LOGIN CREATEROLE PASSWORD '%s'", role, password),
		fmt.Sprintf("GRANT CREATE ON DATABASE %s TO %s", db, role),
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	agentID := "77777777-aaaa-bbbb-cccc-" + fmt.Sprintf("%012d", time.Now().UnixNano()%1e12)
	orphan, _ := provision.NameFor(agentID)
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), testutil.DatabaseURL())
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+orphan)
		_, _ = c.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	u.User = url.UserPassword(role, password)
	agents, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer agents.Close()
	if _, err := provision.New(agents).Create(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "DROP SCHEMA "+orphan+" CASCADE"); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DatabaseURL, cfg.AppURL, cfg.DevAuth = dbURL, "http://localhost:8098", true
	cfg.EncryptionKey = "MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	cfg.APIKeyPepper = cfg.EncryptionKey
	cfg.Agents.DatabaseURL = u.String()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	exists := func() bool {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = $1", orphan).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}

	dropped, _, err := app.PruneRoles(ctx, cfg, log, true)
	if err != nil || !slices.Contains(dropped, orphan) || !exists() {
		t.Fatalf("dry run: %v, %v (role exists: %v)", dropped, err, exists())
	}
	dropped, _, err = app.PruneRoles(ctx, cfg, log, false)
	if err != nil || !slices.Contains(dropped, orphan) || exists() {
		t.Fatalf("prune: %v, %v (role exists: %v)", dropped, err, exists())
	}
}
