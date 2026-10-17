// Package ratelimit counts requests in fixed one-minute windows: sign-in
// attempts per client address and /v1 requests per person.
//
// Store is where the counters live. PostgresStore keeps them in the
// UNLOGGED table rate_limit_counters, so every replica shares them (no
// Valkey; a Valkey store can implement the same interface later).
// Windows are aligned to the database clock (date_trunc('minute', now())).
// Limiter keeps them in this process's memory and stays for tests and
// tools; with several replicas each would enforce a limit on its own share.
//
// Buckets are opaque strings of at most 200 bytes. Callers never put a raw
// client address in one: Bucket hashes it.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Window is the length of a counting window.
const Window = time.Minute

// Count is one bucket's count in the current window.
type Count struct {
	Bucket string
	Count  int64
}

// Store counts events.
type Store interface {
	// Hit counts one event in the current window of every bucket and
	// returns their counts (in the order of buckets) and how long until the
	// window ends.
	Hit(ctx context.Context, buckets []string) (counts []int64, reset time.Duration, err error)
}

// Bucket is a bucket name of a kind and a value that may be personal (a
// client address): the value is hashed (SHA-256, hex), never stored.
func Bucket(kind, value string) string {
	sum := sha256.Sum256([]byte(value))
	return kind + ":" + hex.EncodeToString(sum[:])
}

// Allow counts one event for bucket and reports whether it is within limit
// events per window, and if not, how long until the window ends. A limit of
// 0 or less allows everything without counting.
func Allow(ctx context.Context, s Store, bucket string, limit int) (bool, time.Duration, error) {
	if limit <= 0 {
		return true, 0, nil
	}
	counts, reset, err := s.Hit(ctx, []string{bucket})
	if err != nil {
		return false, 0, err
	}
	if counts[0] > int64(limit) {
		return false, reset, nil
	}
	return true, reset, nil
}

// RetryAfter is reset in whole seconds, at least 1 (for Retry-After).
func RetryAfter(reset time.Duration) int {
	return max(int((reset+time.Second-1)/time.Second), 1)
}

// PostgresStore keeps counters in PostgreSQL.
type PostgresStore struct {
	q *dbgen.Queries
}

// NewPostgres returns a store using pool.
func NewPostgres(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{q: dbgen.New(pool)} }

var errBucket = errors.New("ratelimit: invalid bucket")

// Hit upserts every bucket in one statement (sorted, so concurrent requests
// lock rows in the same order).
func (s *PostgresStore) Hit(ctx context.Context, buckets []string) ([]int64, time.Duration, error) {
	if len(buckets) == 0 {
		return nil, Window, nil
	}
	for _, b := range buckets {
		if b == "" || len(b) > 200 {
			return nil, 0, errBucket
		}
	}
	uniq := slices.Clone(buckets)
	slices.Sort(uniq)
	uniq = slices.Compact(uniq)
	rows, err := s.q.HitRateLimits(ctx, uniq)
	if err != nil {
		return nil, 0, err
	}
	byBucket := make(map[string]int64, len(rows))
	reset := Window
	for _, r := range rows {
		byBucket[r.Bucket] = int64(r.Count)
		reset = time.Duration(r.ResetSeconds * float64(time.Second))
	}
	out := make([]int64, len(buckets))
	for i, b := range buckets {
		out[i] = byBucket[b]
	}
	return out, max(reset, 0), nil
}

// Cleanup deletes windows older than two minutes. Any replica may run it.
func (s *PostgresStore) Cleanup(ctx context.Context) (int64, error) {
	return s.q.DeleteOldRateLimitCounters(ctx)
}

// maxTracked bounds the in-memory map; past it, expired windows are swept.
const maxTracked = 10_000

// Limiter is an in-memory Store with windows aligned to the clock's
// minutes. The zero value is not usable: use New.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	start time.Time
	count int64
}

// New returns an empty in-memory limiter.
func New() *Limiter { return NewWithClock(time.Now) }

// NewWithClock returns an empty in-memory limiter reading now (tests).
func NewWithClock(now func() time.Time) *Limiter {
	return &Limiter{windows: map[string]*window{}, now: now}
}

// Hit counts one event in every bucket's current window.
func (l *Limiter) Hit(_ context.Context, buckets []string) ([]int64, time.Duration, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	start := now.Truncate(Window)
	out := make([]int64, len(buckets))
	for i, key := range buckets {
		w, ok := l.windows[key]
		if !ok || !w.start.Equal(start) {
			if !ok && len(l.windows) >= maxTracked {
				for k, old := range l.windows {
					if old.start.Before(start) {
						delete(l.windows, k)
					}
				}
			}
			w = &window{start: start}
			l.windows[key] = w
		}
		w.count++
		out[i] = w.count
	}
	return out, start.Add(Window).Sub(now), nil
}

// Allow is the package's Allow on l, for callers without a context.
func (l *Limiter) Allow(key string, limit int, _ time.Duration) (bool, time.Duration) {
	ok, wait, _ := Allow(context.Background(), l, key, limit)
	return ok, wait
}
