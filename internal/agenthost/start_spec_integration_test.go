package agenthost_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
)

// currentSpec stands in for the agents service's Resolve: the agent's spec as
// the database has it now.
type currentSpec struct {
	mu   sync.Mutex
	spec launcher.Spec
	err  error
}

func (c *currentSpec) set(s launcher.Spec, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.spec, c.err = s, err
}

func (c *currentSpec) get(context.Context, string) (launcher.Spec, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spec, c.err
}

// teamSpec is agent-1's spec while it belongs to team (its model key, say).
func teamSpec(team string) launcher.Spec {
	return launcher.Spec{AgentID: "agent-1", Token: "token", Env: map[string]string{"MODEL_API_KEY": team + "-key"}}
}

func (r *replicas) startedWith() (bool, string) {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	return r.c.pods["agent-1"], r.c.specs["agent-1"].Env["MODEL_API_KEY"]
}

// A runtime starts with the agent's spec as it is when it starts, not as it
// was when the request that wakes it was authorized: a request that built its
// spec before a move and wakes the agent after the move stopped it starts it
// with the new team's configuration (model key, MCP servers), not the old
// team's. And an agent that is gone by then is not started.
func TestStartUsesTheAgentsCurrentSpec(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withLocks bool
	}{{"one process, no locker", false}, {"replicas with the locker", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := newReplicas(t, tc.withLocks)
			r.c.specs = map[string]launcher.Spec{}
			cur := &currentSpec{spec: teamSpec("old")}
			r.a.Resolve, r.b.Resolve = cur.get, cur.get

			stale := teamSpec("old") // a request authorized before the move
			if _, err := r.a.Wake(ctx, stale); err != nil {
				t.Fatal(err)
			}
			cur.set(teamSpec("new"), nil) // the move commits
			if err := r.b.Hibernate(ctx, "agent-1"); err != nil {
				t.Fatal(err)
			}
			if _, err := r.a.Wake(ctx, stale); err != nil { // the request in flight wakes it
				t.Fatal(err)
			}
			if running, key := r.startedWith(); !running || key != "new-key" {
				t.Fatalf("restarted after the move: running %v with %q, want the new team's key", running, key)
			}

			// Deleted (or otherwise unresolvable) meanwhile: nothing starts.
			if err := r.b.Hibernate(ctx, "agent-1"); err != nil {
				t.Fatal(err)
			}
			cur.set(launcher.Spec{}, errors.New("agent not found"))
			if _, err := r.a.Wake(ctx, stale); err == nil {
				t.Fatal("started an agent its spec could not be resolved for")
			}
			if running, _ := r.startedWith(); running {
				t.Fatal("an agent that could not be resolved is running")
			}
		})
	}
}

// A move's stop waits for a start already under way (which read the agent
// before the move committed) and stops what it started: no runtime with the
// old team's configuration outlives the move, whichever replica stops it.
func TestStopWaitsForAStartUnderWay(t *testing.T) {
	for _, tc := range []struct {
		name         string
		withLocks    bool
		otherReplica bool
	}{
		{"one process, no locker", false, false},
		{"one replica", true, false},
		{"two replicas", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := newReplicas(t, tc.withLocks)
			r.c.specs = map[string]launcher.Spec{}
			r.c.delay = 300 * time.Millisecond
			cur := &currentSpec{spec: teamSpec("old")}
			r.a.Resolve, r.b.Resolve = cur.get, cur.get
			begun := make(chan struct{})
			var once sync.Once
			r.c.starting = func(launcher.Spec) { once.Do(func() { close(begun) }) }

			woke := make(chan error, 1)
			go func() {
				_, err := r.a.Wake(ctx, teamSpec("old"))
				woke <- err
			}()
			<-begun // the start read the agent before the move
			cur.set(teamSpec("new"), nil)
			stopper := r.a
			if tc.otherReplica {
				stopper = r.b
			}
			if err := stopper.Hibernate(ctx, "agent-1"); err != nil { // the move's stop
				t.Fatal(err)
			}
			if err := <-woke; err != nil {
				t.Fatal(err)
			}
			if running, key := r.startedWith(); running {
				t.Fatalf("a runtime started with %q outlived the move's stop", key)
			}
		})
	}
}
