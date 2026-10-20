package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/app"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// clusterOfOne stands in for the Kubernetes API: an agent marked running has
// its runtime at url, listed without its token (Resolve finds it).
type clusterOfOne struct {
	mu      sync.Mutex
	url     string
	running map[string]bool
	stops   int
}

func (c *clusterOfOne) Ensure(_ context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running[spec.AgentID] = true
	return launcher.Endpoint{BaseURL: c.url, Token: spec.Token}, nil
}
func (c *clusterOfOne) Lookup(_ context.Context, spec launcher.Spec) (launcher.Endpoint, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return launcher.Endpoint{BaseURL: c.url, Token: spec.Token}, c.running[spec.AgentID], nil
}
func (c *clusterOfOne) Stop(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running[id] {
		delete(c.running, id)
		c.stops++
	}
	return nil
}
func (c *clusterOfOne) Remove(ctx context.Context, id string) error { return c.Stop(ctx, id) }
func (c *clusterOfOne) Running() map[string]launcher.Endpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]launcher.Endpoint{}
	for id := range c.running {
		out[id] = launcher.Endpoint{BaseURL: c.url}
	}
	return out
}
func (c *clusterOfOne) Kind() string { return "test" }

func (c *clusterOfOne) stopped() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stops
}

// workingRuntime reports background work at /status (contract version 2) and
// aborts it on POST /background/abort, recording the bearer token and the age.
type workingRuntime struct {
	mu      sync.Mutex
	started time.Time // the oldest background work (zero: none)
	tokens  map[string]bool
	aborts  []int64
}

func (rt *workingRuntime) serve(w http.ResponseWriter, r *http.Request) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.tokens[r.Header.Get("Authorization")] = true
	bg := map[string]any{"tasks": 0, "oldestStartedAt": nil, "waitingForPerson": 1}
	if !rt.started.IsZero() {
		bg["tasks"], bg["oldestStartedAt"] = 2, rt.started.UnixMilli()
	}
	switch r.URL.Path {
	case "/status":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"busyThreads": 0, "openStreams": 0,
			"lastActivityAt": time.Now().Add(-time.Hour).UnixMilli(), "backgroundWork": bg, "contractVersion": 2}})
	case "/background/abort":
		var in struct{ OlderThanMs int64 }
		_ = json.NewDecoder(r.Body).Decode(&in)
		rt.aborts = append(rt.aborts, in.OlderThanMs)
		aborted := 0
		if !rt.started.IsZero() && time.Since(rt.started) >= time.Duration(in.OlderThanMs)*time.Millisecond {
			aborted, rt.started = 2, time.Time{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"aborted": aborted, "settled": true}})
	default:
		http.NotFound(w, r)
	}
}

func (rt *workingRuntime) abortsSeen() []int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return slices.Clone(rt.aborts)
}

// An agent with background work is kept awake, and its page says why; past
// the ceiling (AGENTS_BACKGROUND_MAX) another replica's reaper has the runtime
// abort the work, records it in the team's audit log and counts it, and
// hibernates the agent.
func TestBackgroundWorkKeepsAnAgentAwakeUntilTheCeiling(t *testing.T) {
	rt := &workingRuntime{tokens: map[string]bool{}, started: time.Now().Add(-10 * time.Minute)}
	srv := httptest.NewServer(http.HandlerFunc(rt.serve))
	t.Cleanup(srv.Close)
	cluster := &clusterOfOne{url: srv.URL, running: map[string]bool{}}
	mutate := func(c *config.Config) { c.Agents.BackgroundMax = time.Hour }
	a := newTestAppWith(t, mutate, testutil.Logger(), nil, false, app.WithLauncher(cluster))
	e := newTeamEnvOn(t, a)
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Worker"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	id := ag.Id.String()
	ctx := context.Background()
	spec, err := a.Agents.Spec(ctx, authz.Actor{UserID: e.editor.me.User.Id}, ag.Id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Agents.Host().Wake(ctx, spec); err != nil {
		t.Fatal(err)
	}

	// The agent page: awake, for its background work (and a question waits for someone).
	var got apitypes.Agent
	if code := e.member.get("/v1/agents/"+id, &got); code != 200 {
		t.Fatalf("get agent = %d", code)
	}
	if !got.Running || got.Awake == nil || !slices.Equal(got.Awake.Reasons, []apitypes.AgentAwakeReasons{"background"}) ||
		got.Awake.BackgroundTasks != 2 || got.Awake.WaitingForPerson != 1 || got.Awake.OverCeiling ||
		got.Awake.BackgroundSince == nil || got.Awake.BackgroundSince.Sub(rt.started).Abs() > time.Millisecond {
		t.Fatalf("agent awake = %+v (running %v)", got.Awake, got.Running)
	}
	// Lists don't ask every runtime.
	var list []apitypes.Agent
	if code := e.member.get(toolsTeam+"/agents", &list); code != 200 || len(list) != 1 || list[0].Awake != nil {
		t.Fatalf("list = %d %+v", code, list)
	}

	// Other replicas (no hints of their own) reap. Under the ceiling, nothing happens.
	pool, err := pgxpool.NewWithConfig(ctx, a.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	replica := func() httpapi.Deps {
		d, err := app.NewDeps(a.cfg, pool, testutil.Logger(), app.WithLauncher(cluster))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Agents.Close)
		return d
	}
	replica().Agents.Host().ReapOnce(ctx)
	if n, aborts := cluster.stopped(), rt.abortsSeen(); n != 0 || len(aborts) != 0 {
		t.Fatalf("under the ceiling: stops %d, aborts %v", n, aborts)
	}

	// Past it.
	rt.mu.Lock()
	rt.started = time.Now().Add(-2 * time.Hour)
	rt.mu.Unlock()
	other := replica()
	other.Agents.Host().ReapOnce(ctx)
	if n, aborts := cluster.stopped(), rt.abortsSeen(); n != 1 || !slices.Equal(aborts, []int64{time.Hour.Milliseconds()}) {
		t.Fatalf("past the ceiling: stops %d, aborts %v", n, aborts)
	}
	rt.mu.Lock()
	tokens := len(rt.tokens)
	ours := rt.tokens["Bearer "+spec.Token]
	rt.mu.Unlock()
	if !ours || tokens != 1 {
		t.Fatalf("the runtime was called with %d tokens, not only the agent's", tokens)
	}
	var actor, team, aborted, ceiling string
	if err := a.Pool.QueryRow(ctx, `SELECT actor_kind, team_id::text, metadata->>'aborted', metadata->>'ceiling' FROM audit_log
		WHERE action = 'agent.background_abort' AND target_type = 'agent' AND target_id = $1`, id).Scan(&actor, &team, &aborted, &ceiling); err != nil {
		t.Fatalf("the audit entry: %v", err)
	}
	if actor != "system" || team != ag.TeamId.String() || aborted != "2" || ceiling != "1h0m0s" {
		t.Fatalf("audit entry: actor %s, team %s, aborted %s, ceiling %s", actor, team, aborted, ceiling)
	}
	if got := metricstest.Gather(t, other.Metrics).Sum("pdp_agent_background_aborts_total", "result", "ok"); got != 1 {
		t.Fatalf("background aborts = %v", got)
	}
	// Hibernated: the page no longer asks the runtime.
	var after apitypes.Agent
	if code := e.member.get("/v1/agents/"+id, &after); code != 200 || after.Running || after.Awake != nil {
		t.Fatalf("after: %d running %v awake %+v", code, after.Running, after.Awake)
	}
}
