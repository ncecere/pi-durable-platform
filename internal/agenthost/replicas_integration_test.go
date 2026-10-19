package agenthost_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// cluster stands in for what replicas share in production: the Kubernetes
// API with its pods. Each replica's launcher is a view of it. Starting is
// deliberately a check, a pause and a create, with no atomicity of its own:
// only the manager's lock keeps two replicas from both starting an agent.
type cluster struct {
	mu     sync.Mutex
	pods   map[string]bool // running agents
	starts atomic.Int64
	stops  atomic.Int64
	delay  time.Duration
	url    string // every agent's runtime

	// Running's concurrent callers (a reaper pass makes one call).
	inRunning, maxRunning atomic.Int64
	runningDelay          time.Duration
}

func (c *cluster) view() launcher.Launcher { return &clusterLauncher{c} }

type clusterLauncher struct{ c *cluster }

func (l *clusterLauncher) endpoint(spec launcher.Spec) launcher.Endpoint {
	return launcher.Endpoint{BaseURL: l.c.url, Token: spec.Token}
}

func (l *clusterLauncher) Lookup(_ context.Context, spec launcher.Spec) (launcher.Endpoint, bool, error) {
	l.c.mu.Lock()
	defer l.c.mu.Unlock()
	return l.endpoint(spec), l.c.pods[spec.AgentID], nil
}

func (l *clusterLauncher) Ensure(_ context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	l.c.mu.Lock()
	running := l.c.pods[spec.AgentID]
	l.c.mu.Unlock()
	if !running {
		time.Sleep(l.c.delay) // the pod starts
		l.c.mu.Lock()
		l.c.pods[spec.AgentID] = true
		l.c.mu.Unlock()
		l.c.starts.Add(1)
	}
	return l.endpoint(spec), nil
}

func (l *clusterLauncher) Stop(_ context.Context, agentID string) error {
	l.c.mu.Lock()
	defer l.c.mu.Unlock()
	if l.c.pods[agentID] {
		delete(l.c.pods, agentID)
		l.c.stops.Add(1)
	}
	return nil
}

func (l *clusterLauncher) Remove(ctx context.Context, agentID string) error {
	return l.Stop(ctx, agentID)
}
func (l *clusterLauncher) Kind() string { return "cluster" }

func (l *clusterLauncher) Running() map[string]launcher.Endpoint {
	n := l.c.inRunning.Add(1)
	defer l.c.inRunning.Add(-1)
	for {
		m := l.c.maxRunning.Load()
		if n <= m || l.c.maxRunning.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(l.c.runningDelay)
	l.c.mu.Lock()
	defer l.c.mu.Unlock()
	out := map[string]launcher.Endpoint{}
	for id := range l.c.pods {
		out[id] = launcher.Endpoint{BaseURL: l.c.url} // as Kubernetes: no token
	}
	return out
}

// runtime answers /status as an agent's runtime does, with whatever the test
// sets, and counts the questions.
type fakeRuntime struct {
	srv         *httptest.Server
	mu          sync.Mutex
	busy        int
	openStreams *int // nil: a runtime that does not report it
	lastActive  time.Time
	asked       atomic.Int64
}

func newFakeRuntime(t *testing.T) *fakeRuntime {
	t.Helper()
	r := &fakeRuntime{lastActive: time.Now().Add(-time.Hour)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/status" || req.Header.Get("Authorization") != "Bearer token" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		r.asked.Add(1)
		r.mu.Lock()
		body := map[string]any{"busyThreads": r.busy, "lastActivityAt": r.lastActive.UnixMilli()}
		if r.openStreams != nil {
			body["openStreams"] = *r.openStreams
		}
		r.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"data": body})
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *fakeRuntime) set(busy int, streams *int, lastActive time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.busy, r.openStreams, r.lastActive = busy, streams, lastActive
}

func intp(n int) *int { return &n }

type replicas struct {
	c       *cluster
	rt      *fakeRuntime
	a, b    *agenthost.Manager
	locks   store.Locks
	spec    launcher.Spec
	idle    time.Duration
	started time.Time
}

// newReplicas is two managers on one cluster and one database. withLocks
// false leaves them uncoordinated (the control).
func newReplicas(t *testing.T, withLocks bool) *replicas {
	t.Helper()
	pool, _ := testutil.NewDB(t)
	r := &replicas{rt: newFakeRuntime(t), locks: store.Locks{Pool: pool}, idle: time.Minute,
		spec: launcher.Spec{AgentID: "agent-1", Token: "token"}}
	r.c = &cluster{pods: map[string]bool{}, url: r.rt.srv.URL, delay: 200 * time.Millisecond}
	mk := func() *agenthost.Manager {
		return r.manager(withLocks)
	}
	r.a, r.b = mk(), mk()
	return r
}

// manager is a replica's manager (Resolve finds a runtime's token, as the
// agents service does after a restart: the cluster doesn't list tokens).
func (r *replicas) manager(withLocks bool) *agenthost.Manager {
	m := agenthost.New(r.c.view(), r.idle, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Resolve = func(context.Context, string) (launcher.Spec, error) { return r.spec, nil }
	if withLocks {
		m.SetLocker(r.locks)
	}
	return m
}

// Waking an agent from two replicas at once starts one runtime: the second
// waits for the database lock and then finds the first one's. Without the
// lock the same test starts it twice, so the fake would catch a regression.
func TestConcurrentWakeStartsOneRuntime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		withLocks bool
		want      func(int64) bool
	}{
		{"with the lock", true, func(n int64) bool { return n == 1 }},
		{"control: no lock", false, func(n int64) bool { return n > 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newReplicas(t, tc.withLocks)
			var wg sync.WaitGroup
			errs := make(chan error, 16)
			for i := range 16 {
				m := r.a
				if i%2 == 1 {
					m = r.b
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					ep, err := m.Wake(context.Background(), r.spec)
					if err == nil && ep.BaseURL != r.c.url {
						err = io.ErrUnexpectedEOF
					}
					errs <- err
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("wake: %v", err)
				}
			}
			if n := r.c.starts.Load(); !tc.want(n) {
				t.Fatalf("runtimes started = %d", n)
			}
			// A running agent costs no lock: waking it again starts nothing.
			before := r.c.starts.Load()
			if _, err := r.b.Wake(context.Background(), r.spec); err != nil || r.c.starts.Load() != before {
				t.Fatalf("wake of a running agent: %v, starts %d -> %d", err, before, r.c.starts.Load())
			}
		})
	}
}

// A wake that waits for another replica's start gives up when its request
// ends, and starts nothing.
func TestWakeWaitsForTheLockUntilItsContextEnds(t *testing.T) {
	r := newReplicas(t, true)
	release, err := r.locks.Lock(context.Background(), "pdp.agent-wake:"+r.spec.AgentID) // another replica is starting it
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.a.Wake(ctx, r.spec); err == nil {
		t.Fatal("woke while another replica held the start lock")
	}
	if time.Since(start) > 3*time.Second || r.c.starts.Load() != 0 {
		t.Fatalf("waited %v, starts %d", time.Since(start), r.c.starts.Load())
	}
	// The holder finishes (or dies): the lock is free again.
	release()
	if _, err := r.b.Wake(context.Background(), r.spec); err != nil || r.c.starts.Load() != 1 {
		t.Fatalf("wake after the lock was released: %v, starts %d", err, r.c.starts.Load())
	}
}

// The reaper believes the runtime, not what its own replica has seen: an
// agent whose stream is open on another replica, or that is at work, is not
// hibernated by a replica that has never heard of it.
func TestReaperAsksTheRuntime(t *testing.T) {
	r := newReplicas(t, true)
	if _, err := r.a.Wake(context.Background(), r.spec); err != nil { // replica A woke it; B never saw it
		t.Fatal(err)
	}
	long := time.Now().Add(-time.Hour)
	ctx := context.Background()
	hibernated := func() bool { return r.c.stops.Load() > 0 }

	// A stream is open (on A, say), though the runtime was last used long ago.
	r.rt.set(0, intp(1), long)
	r.b.ReapOnce(ctx)
	r.a.ReapOnce(ctx)
	if hibernated() {
		t.Fatal("an agent with an open stream was hibernated")
	}
	// At work.
	r.rt.set(1, intp(0), long)
	r.b.ReapOnce(ctx)
	if hibernated() {
		t.Fatal("an agent at work was hibernated")
	}
	// Used a moment ago on some replica.
	r.rt.set(0, intp(0), time.Now())
	r.b.ReapOnce(ctx)
	if hibernated() {
		t.Fatal("a recently used agent was hibernated")
	}
	// Idle. (Hints from the checks above let this replica skip the agent
	// until it could be idle: a fresh manager has none.)
	r.rt.set(0, intp(0), long)
	r.b = r.manager(true)
	r.b.ReapOnce(ctx)
	if r.c.stops.Load() != 1 {
		t.Fatalf("an idle agent: stops = %d, want 1", r.c.stops.Load())
	}
	r.a.ReapOnce(ctx) // already gone: nothing to do
	if r.c.stops.Load() != 1 {
		t.Fatalf("stops = %d after the second replica's pass", r.c.stops.Load())
	}
}

// A runtime that does not report its streams (started before an upgrade) is
// judged as before: by its work and its last activity.
func TestReaperHandlesARuntimeWithoutStreamCounts(t *testing.T) {
	r := newReplicas(t, true)
	if _, err := r.a.Wake(context.Background(), r.spec); err != nil {
		t.Fatal(err)
	}
	r.rt.set(0, nil, time.Now().Add(-time.Hour))
	r.b.ReapOnce(context.Background())
	if r.c.stops.Load() != 1 {
		t.Fatalf("stops = %d", r.c.stops.Load())
	}
}

// The reaper does not ask a runtime again until it could be idle.
func TestReaperSkipsAgentsThatCannotBeIdleYet(t *testing.T) {
	r := newReplicas(t, true)
	if _, err := r.a.Wake(context.Background(), r.spec); err != nil {
		t.Fatal(err)
	}
	r.rt.set(0, intp(0), time.Now())
	for range 3 {
		r.b.ReapOnce(context.Background())
	}
	if n := r.rt.asked.Load(); n != 1 {
		t.Fatalf("the runtime was asked %d times in three passes, want 1", n)
	}
}

// Only one replica reaps at a time: a pass that finds another replica
// reaping does nothing, and an idle agent is hibernated once.
func TestOneReaperAtATime(t *testing.T) {
	r := newReplicas(t, true)
	if _, err := r.manager(true).Wake(context.Background(), r.spec); err != nil { // started some time ago
		t.Fatal(err)
	}
	r.rt.set(0, intp(0), time.Now().Add(-time.Hour))
	r.c.runningDelay = 300 * time.Millisecond // a pass takes a while
	var wg sync.WaitGroup
	for _, m := range []*agenthost.Manager{r.a, r.b, r.a, r.b} {
		wg.Add(1)
		go func() { defer wg.Done(); m.ReapOnce(context.Background()) }()
	}
	wg.Wait()
	if n := r.c.maxRunning.Load(); n != 1 {
		t.Fatalf("%d reaper passes ran at the same time, want 1", n)
	}
	if r.c.stops.Load() != 1 {
		t.Fatalf("stops = %d", r.c.stops.Load())
	}
}

// A replica whose reaper holds the lock and dies (its connection ends) hands
// the job to the next pass of another replica.
func TestReaperLockEndsWithItsHolder(t *testing.T) {
	r := newReplicas(t, true)
	release, ok, err := r.locks.TryLock(context.Background(), "pdp.agent-reaper")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if _, err := r.a.Wake(context.Background(), r.spec); err != nil {
		t.Fatal(err)
	}
	r.rt.set(0, intp(0), time.Now().Add(-time.Hour))
	r.b.ReapOnce(context.Background())
	if r.c.stops.Load() != 0 {
		t.Fatal("reaped while another replica held the reaper lock")
	}
	release() // the holder is gone
	r.b.ReapOnce(context.Background())
	if r.c.stops.Load() != 1 {
		t.Fatalf("stops = %d after the lock was released", r.c.stops.Load())
	}
}
