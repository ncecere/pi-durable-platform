// Package testutil provides real-infrastructure helpers for integration tests.
//
// Tests needing PostgreSQL call NewDB, which creates a fresh, migrated
// database per test. The server comes from PDP_TEST_DATABASE_URL (default:
// the compose Postgres, postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres).
// When it is unreachable the test is skipped, except in CI (CI=true) or with
// PDP_TEST_REQUIRE_DB=1, where a missing database fails the test instead of
// silently skipping it.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/store"
)

// DefaultDatabaseURL is the compose Postgres (make deps-up), as a role
// allowed to create databases.
const DefaultDatabaseURL = "postgres://pdp:pdp-local-only@127.0.0.1:55432/postgres?sslmode=disable"

// DatabaseURL is PDP_TEST_DATABASE_URL, or DefaultDatabaseURL.
func DatabaseURL() string {
	if v := os.Getenv("PDP_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return DefaultDatabaseURL
}

func requireDB() bool {
	return os.Getenv("CI") == "true" || os.Getenv("PDP_TEST_REQUIRE_DB") == "1"
}

var (
	reachOnce sync.Once
	reachErr  error
)

// adminConn connects to the test server, skipping (or failing, see the
// package comment) when it is unreachable. Reachability is checked once per
// test binary so a missing database costs one connection timeout.
func adminConn(t testing.TB, ctx context.Context) *pgx.Conn {
	t.Helper()
	reachOnce.Do(func() {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		c, err := pgx.Connect(cctx, DatabaseURL())
		if err == nil {
			_ = c.Close(cctx)
		}
		reachErr = err
	})
	if reachErr != nil {
		if requireDB() {
			t.Fatalf("test database unreachable (%v); run `make deps-up`", reachErr)
		}
		t.Skipf("test database unreachable; skipping integration test (run `make deps-up`): %v", reachErr)
	}
	admin, err := pgx.Connect(ctx, DatabaseURL())
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	return admin
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Logger discards output unless PDP_TEST_LOG=1.
func Logger() *slog.Logger {
	if os.Getenv("PDP_TEST_LOG") == "1" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// NewDB creates a fresh, fully migrated database for one test and drops it
// afterwards.
func NewDB(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	admin := adminConn(t, ctx)
	name := "pdp_test_" + randomSuffix()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	_ = admin.Close(ctx)

	u, err := url.Parse(DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dbURL := u.String()
	poolURL := dbURL
	if n := os.Getenv("PDP_TEST_POOL_MAX_CONNS"); n != "" {
		// A pool as small as a small CI runner's (pgxpool defaults to max(4, CPUs)), to find tests and code that
		// need more connections than they hold.
		q := u.Query()
		q.Set("pool_max_conns", n)
		pu := *u
		pu.RawQuery = q.Encode()
		poolURL = pu.String()
	}
	pool, err := store.Open(ctx, poolURL)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(ctx, pool, Logger()); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		admin, err := pgx.Connect(ctx, DatabaseURL())
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Logf("drop test database: %v", err)
		}
	})
	return pool, dbURL
}
