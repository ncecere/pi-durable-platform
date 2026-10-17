package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// Two stores on one database (two replicas) share their counters; old
// windows are deleted.
func TestPostgresStoreIsShared(t *testing.T) {
	ctx := context.Background()
	pool, _ := testutil.NewDB(t)
	a, b := ratelimit.NewPostgres(pool), ratelimit.NewPostgres(pool)
	var left float64
	_ = pool.QueryRow(ctx, `SELECT extract(epoch FROM date_trunc('minute', now()) + interval '1 minute' - now())::float8`).Scan(&left)
	if left < 5 {
		time.Sleep(time.Duration(left*float64(time.Second)) + 100*time.Millisecond)
	}
	counts, reset, err := a.Hit(ctx, []string{"y", "x"})
	if err != nil || counts[0] != 1 || counts[1] != 1 || reset <= 0 || reset > time.Minute {
		t.Fatalf("first = %v %v %v", counts, reset, err)
	}
	counts, _, _ = b.Hit(ctx, []string{"x", "x"})
	if counts[0] != 2 || counts[1] != 2 {
		t.Fatalf("shared = %v", counts)
	}
	ok, wait, err := ratelimit.Allow(ctx, b, "x", 2)
	if ok || err != nil || wait <= 0 {
		t.Fatalf("over the limit = %v %v %v", ok, wait, err)
	}
	if _, _, err := a.Hit(ctx, []string{string(make([]byte, 201))}); err == nil {
		t.Fatal("an oversized bucket is refused")
	}
	if _, err := pool.Exec(ctx, `UPDATE rate_limit_counters SET window_start = window_start - interval '5 minutes' WHERE bucket = 'y'`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.Cleanup(ctx); err != nil || n != 1 {
		t.Fatalf("cleanup = %d %v", n, err)
	}
}
