package app

import (
	"context"
	"io"
	"log/slog"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Replicas that start together and find no agents database (development:
// AGENTS_CREATE_DATABASE) create it once and none of them fails.
func TestEnsureAgentsDatabaseWithSeveralReplicas(t *testing.T) {
	pool, dbURL := testutil.NewDB(t)
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	name := u.Path[1:] + "_agents"
	u.Path = "/" + name
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if admin, err := pgx.Connect(ctx, testutil.DatabaseURL()); err == nil {
			defer admin.Close(ctx)
			_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		}
	})

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- ensureAgentsDatabase(context.Background(), pool, u.String(), log)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ensureAgentsDatabase: %v", err)
		}
	}
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM pg_database WHERE datname = $1", name).Scan(&n); err != nil || n != 1 {
		t.Fatalf("agents database count = %d, %v", n, err)
	}
}
