package httpapi_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/app"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// recordingLauncher records the spec each start was given; nothing runs.
type recordingLauncher struct {
	mu      sync.Mutex
	started []launcher.Spec
}

func (l *recordingLauncher) Ensure(_ context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = append(l.started, spec)
	return launcher.Endpoint{BaseURL: "http://127.0.0.1:9", Token: spec.Token}, nil
}
func (l *recordingLauncher) Lookup(context.Context, launcher.Spec) (launcher.Endpoint, bool, error) {
	return launcher.Endpoint{}, false, nil
}
func (l *recordingLauncher) Stop(context.Context, string) error   { return nil }
func (l *recordingLauncher) Remove(context.Context, string) error { return nil }
func (l *recordingLauncher) Running() map[string]launcher.Endpoint {
	return map[string]launcher.Endpoint{}
}
func (l *recordingLauncher) Kind() string { return "recording" }

// A request authorized before a move that wakes the agent after it starts the
// runtime with the new team's configuration: its model (and the MCP servers
// and keys that come with the team), not the spec the request built with the
// old team's. The waking replica is a second platform instance, as with
// several replicas, whose launcher records what it starts.
func TestStartAfterAMoveUsesTheNewTeamsSpec(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	adminActor := authz.Actor{UserID: e.admin.me.User.Id, PlatformRole: authz.PlatformAdmin}
	prov, err := e.app.Models.CreateProvider(ctx, adminActor, llm.ProviderInput{Name: "upstream", Kind: "openai-compatible",
		BaseURL: "http://127.0.0.1:9/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	toolsOnly, err := e.app.Models.CreateModel(ctx, adminActor, prov.Provider.ID, llm.ModelInput{UpstreamID: "tools-model", Visibility: "teams", Teams: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := e.app.Models.CreateModel(ctx, adminActor, prov.Provider.ID, llm.ModelInput{UpstreamID: "shared-model"})
	if err != nil {
		t.Fatal(err)
	}
	ag := e.agent(toolsTeam, "Stale", map[string]any{"modelId": toolsOnly.Model.ID})

	pool, err := pgxpool.NewWithConfig(ctx, e.app.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rec := &recordingLauncher{}
	other, err := app.NewDeps(e.app.cfg, pool, testutil.Logger(), app.WithLauncher(rec))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Agents.Close)

	// The request is authorized, and its spec built, while the agent is the old team's.
	stale, err := other.Agents.Spec(ctx, authz.Actor{UserID: e.blair.me.User.Id}, ag.Id)
	if err != nil || stale.Env["MODEL_ID"] != "tools-model" {
		t.Fatalf("spec before the move: %v %v", err, stale.Env["MODEL_ID"])
	}
	code, ec := e.user.call("POST", "/v1/agents/"+ag.Id.String()+"/move", map[string]any{"team": "dest", "modelId": everyone.Model.ID},
		nil, ifMatch(ag.Revision))
	mustCode(t, "move", code, ec, 200, "")

	// Then it wakes the agent.
	if _, err := other.Agents.Host().Wake(ctx, stale); err != nil {
		t.Fatal(err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.started) != 1 || rec.started[0].Env["MODEL_ID"] != "shared-model" {
		var got string
		if len(rec.started) > 0 {
			got = rec.started[len(rec.started)-1].Env["MODEL_ID"]
		}
		t.Fatalf("started %d times, with model %q; want the new team's (shared-model)", len(rec.started), got)
	}
}

// MCP servers an app sets on a thread while the agent moves don't survive the
// move: a write that started before the move either finishes before it (and
// the move removes its servers) or, waiting for it, finds the agent in
// another team and is refused as if the agent were not found.
func TestThreadMCPServersRacingAMove(t *testing.T) {
	e := newMoveEnvWith(t, func(c *config.Config) { c.Agents.ClientMCPHosts = "http://127.0.0.1:*" })
	ctx := context.Background()
	// Apps may give the catalog's assistant MCP servers (so it may move to any team).
	if _, err := e.app.Pool.Exec(ctx, `UPDATE agent_template_versions SET client_mcp = true
		WHERE template_id = (SELECT id FROM agent_templates WHERE slug = 'assistant')`); err != nil {
		t.Fatal(err)
	}
	// The test's own connections (the lock it holds, the waits it counts) come from a pool of its own: taken from the
	// app's pool, they could use up its last connection while the app's requests wait for locks, and the test would
	// then wait for a connection forever (a small CI runner has a pool of four).
	probe, err := pgxpool.NewWithConfig(ctx, e.app.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(probe.Close)
	type result struct {
		code int
		ec   string
	}
	servers := func(agentID, thread, name string) <-chan result {
		out := make(chan result, 1)
		go func() {
			code, ec := e.blair.call("PUT", "/v1/agents/"+agentID+"/threads/"+thread+"/mcp-servers", mcpServersBody("", name), nil, nil)
			out <- result{code, ec}
		}()
		return out
	}
	count := func(sql string, args ...any) (n int) {
		t.Helper()
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := probe.QueryRow(qctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// waiting waits until n sessions wait for a lock, or done has an answer (a request that didn't wait).
	waiting := func(n int, done ...<-chan result) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if count(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) >= n {
				return
			}
			for _, d := range done {
				if len(d) > 0 {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("nobody waited for a lock (%d expected)", n)
	}

	t.Run("a write the move waits for", func(t *testing.T) {
		ag := e.agent(toolsTeam, "Waited for", nil)
		id := ag.Id.String()
		if r := <-servers(id, "race", "First"); r.code != 200 {
			t.Fatalf("first servers: %+v", r)
		}
		// The write's insert waits behind a lock on the thread's record (a foreign key check) ...
		tx, err := probe.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM agent_threads WHERE agent_id = $1 AND thread_id = 'race' FOR UPDATE`, ag.Id); err != nil {
			t.Fatal(err)
		}
		write := servers(id, "race", "Second")
		waiting(1, write)
		// ... while the agent moves.
		moved := make(chan result, 1)
		go func() {
			code, ec := e.user.call("POST", "/v1/agents/"+id+"/move", map[string]any{"team": "dest"}, nil, ifMatch(ag.Revision))
			moved <- result{code, ec}
		}()
		waiting(2, moved)
		_ = tx.Rollback(ctx)
		if r := <-moved; r.code != 200 {
			t.Fatalf("move: %+v", r)
		}
		<-write
		if n := count(`SELECT count(*) FROM thread_mcp_servers WHERE agent_id = $1`, ag.Id); n != 0 {
			t.Fatalf("%d MCP servers set by the old team's member survived the move", n)
		}
	})

	t.Run("a write that waits for the move", func(t *testing.T) {
		ag := e.agent(toolsTeam, "Waiting", nil)
		id := ag.Id.String()
		if r := <-servers(id, "race", "First"); r.code != 200 {
			t.Fatalf("first servers: %+v", r)
		}
		// The move under way: the agent's row locked, as Move locks it, then moved and its thread servers removed.
		tx, err := probe.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM agents WHERE id = $1 FOR UPDATE`, ag.Id); err != nil {
			t.Fatal(err)
		}
		write := servers(id, "race", "Second") // authorized while the agent is still the old team's
		waiting(1, write)
		if _, err := tx.Exec(ctx, `UPDATE agents SET team_id = (SELECT id FROM teams WHERE slug = 'dest'), revision = revision + 1 WHERE id = $1`,
			ag.Id); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM thread_mcp_servers WHERE agent_id = $1`, ag.Id); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if r := <-write; r.code != 404 || r.ec != "agent_not_found" {
			t.Fatalf("a write authorized before the move and done after it: %+v, want 404 agent_not_found", r)
		}
		if n := count(`SELECT count(*) FROM thread_mcp_servers WHERE agent_id = $1`, ag.Id); n != 0 {
			t.Fatalf("%d MCP servers set by the old team's member survived the move", n)
		}
	})
}
