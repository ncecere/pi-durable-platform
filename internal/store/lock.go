package store

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TryAdvisoryLock takes the session advisory lock key on a connection of its
// own, opened outside the pool, and reports whether it got it. release
// closes that connection, which releases the lock.
//
// Holding a session lock on a pooled connection while the work under it
// uses the pool can deadlock a small pool: with as many lock holders as
// connections, each one waits for a second connection that never frees up
// (pgxpool's default size is max(4, CPUs)). A dedicated connection keeps the
// pool for the work; it costs one Postgres connection per lock held.
func TryAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, key int64) (release func(), ok bool, err error) {
	return tryLock(ctx, pool, "SELECT pg_try_advisory_lock($1)", key)
}

// TryAdvisoryLockText is TryAdvisoryLock for a text key (hashed with
// hashtextextended(key, 0), as the SQL callers do).
func TryAdvisoryLockText(ctx context.Context, pool *pgxpool.Pool, key string) (release func(), ok bool, err error) {
	return tryLock(ctx, pool, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", key)
}

func tryLock(ctx context.Context, pool *pgxpool.Pool, sql string, key any) (func(), bool, error) {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, false, err
	}
	closeConn := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = conn.Close(ctx) // ends the session and its lock
	}
	var got bool
	if err := conn.QueryRow(ctx, sql, key).Scan(&got); err != nil || !got {
		closeConn()
		return nil, false, err
	}
	return closeConn, true, nil
}

// AdvisoryLockText waits for the session advisory lock key (a text key,
// hashed like TryAdvisoryLockText) on a connection of its own, outside the
// pool, until it has it or ctx ends. release closes that connection, which
// releases the lock; it is safe to call more than once.
func AdvisoryLockText(ctx context.Context, pool *pgxpool.Pool, key string) (release func(), err error) {
	conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
	if err != nil {
		return nil, err
	}
	var once sync.Once
	closeConn := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = conn.Close(ctx) // ends the session and its lock
		})
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(hashtextextended($1, 0))", key); err != nil {
		closeConn()
		return nil, err
	}
	return closeConn, nil
}

// Locks takes advisory locks by name in one database: what several replicas
// of the platform use to agree on who starts an agent or reaps idle ones
// (agenthost.Locker).
type Locks struct{ Pool *pgxpool.Pool }

// Lock waits for the lock called key.
func (l Locks) Lock(ctx context.Context, key string) (func(), error) {
	return AdvisoryLockText(ctx, l.Pool, key)
}

// TryLock takes the lock called key if nobody holds it.
func (l Locks) TryLock(ctx context.Context, key string) (func(), bool, error) {
	return TryAdvisoryLockText(ctx, l.Pool, key)
}
