package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// The lock is exclusive, released by release, and taken on a connection
// outside the pool: it works while every pooled connection is in use.
func TestTryAdvisoryLockOutsideThePool(t *testing.T) {
	pool, _ := testutil.NewDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Hold every pooled connection.
	var held []*pgxpool.Conn
	for range pool.Config().MaxConns {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}

	release, ok, err := store.TryAdvisoryLockText(ctx, pool, "pdp.test-lock")
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%v err=%v (with the pool exhausted)", ok, err)
	}
	if _, again, err := store.TryAdvisoryLockText(ctx, pool, "pdp.test-lock"); err != nil || again {
		t.Fatalf("second lock: ok=%v err=%v, want not taken", again, err)
	}
	release()
	release2, ok, err := store.TryAdvisoryLockText(ctx, pool, "pdp.test-lock")
	if err != nil || !ok {
		t.Fatalf("after release: ok=%v err=%v", ok, err)
	}
	release2()

	releaseInt, ok, err := store.TryAdvisoryLock(ctx, pool, 42)
	if err != nil || !ok {
		t.Fatalf("int key: ok=%v err=%v", ok, err)
	}
	releaseInt()
	for _, c := range held {
		c.Release()
	}
}
