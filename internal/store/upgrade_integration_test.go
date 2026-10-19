package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
	"github.com/ncecere/pi-durable-platform/migrations"
)

// A v0.1.x installation upgraded to the current release: the database is taken back to schema version 16 (the last of
// v0.1.x), filled with what v0.1.x could store, migrated forward, and what the release promises is checked: servers of a
// thread that exists survive, servers of a thread nobody started (the hole #5 closes) are dropped, existing threads are
// not "reserved", nobody is group-denied, and the new rules hold from then on.
func TestUpgradeFromV016KeepsData(t *testing.T) {
	ctx := context.Background()
	pool, _ := testutil.NewDB(t)
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 16); err != nil {
		t.Fatalf("roll back to version 16 (every migration after it needs a working Down): %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	team, agent, user := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO teams (id, slug, name) VALUES ($1, 'upgrade-team', 'Upgrade')`, team)
	exec(`INSERT INTO agents (id, team_id, template_id, template_version, name, db_password_enc, runtime_token_enc)
		SELECT $1, $2, id, 1, 'agent', '\x00', '\x00' FROM agent_templates WHERE slug = 'assistant'`, agent, team)
	exec(`INSERT INTO users (id, oidc_issuer, oidc_subject, email) VALUES ($1, 'https://idp.example.com', 's', 'p@example.com')`, user)
	exec(`INSERT INTO agent_threads (agent_id, thread_id, title) VALUES ($1, 'kept', 'A thread')`, agent)
	server := func(thread, slug string) {
		exec(`INSERT INTO thread_mcp_servers (agent_id, thread_id, name, slug, url, headers_enc, expires_at)
			VALUES ($1, $2, 'Server', $3, 'https://mcp.example.com', '\x00', now() + interval '1 day')`, agent, thread, slug)
	}
	server("kept", "one")
	server("kept", "two")
	server("never-started", "stray") // v0.1.x let an app set servers on an unrecorded thread

	if err := store.Migrate(ctx, pool, testutil.Logger()); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	count := func(sql string, args ...any) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM thread_mcp_servers WHERE thread_id = 'kept'`); n != 2 {
		t.Errorf("servers of a recorded thread: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM thread_mcp_servers WHERE thread_id = 'never-started'`); n != 0 {
		t.Errorf("servers of an unrecorded thread survived: %d", n)
	}
	if n := count(`SELECT count(*) FROM agent_threads WHERE agent_id = $1 AND thread_id = 'kept' AND NOT reserved`, agent); n != 1 {
		t.Error("an existing thread is marked reserved")
	}
	if n := count(`SELECT count(*) FROM users WHERE id = $1 AND group_denied_at IS NULL AND group_denied_reason IS NULL`, user); n != 1 {
		t.Error("an existing person is group-denied after the upgrade")
	}
	if n := count(`SELECT count(*) FROM user_idp_tokens`); n != 0 {
		t.Errorf("%d IdP tokens appeared out of nowhere (nobody has signed in since the upgrade, so nobody is re-checked)", n)
	}

	// From now on a thread's servers hang off its record, and go with it.
	if _, err := pool.Exec(ctx, `INSERT INTO thread_mcp_servers (agent_id, thread_id, name, slug, url, expires_at)
		VALUES ($1, 'never-started', 'Server', 'again', 'https://mcp.example.com', now() + interval '1 day')`, agent); err == nil {
		t.Error("servers were accepted for a thread that has no record")
	}
	exec(`DELETE FROM agent_threads WHERE agent_id = $1 AND thread_id = 'kept'`, agent)
	if n := count(`SELECT count(*) FROM thread_mcp_servers WHERE agent_id = $1`, agent); n != 0 {
		t.Errorf("deleting a thread left %d of its servers", n)
	}
	// And the migration is complete: running it again does nothing.
	if err := store.Migrate(ctx, pool, testutil.Logger()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
