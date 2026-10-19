package agenthost

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
)

// scriptedLauncher answers Ensure with err, and lists running agents.
type scriptedLauncher struct {
	mu      sync.Mutex
	ensures int
	err     error
	running map[string]launcher.Endpoint
	stopped []string
	stopErr error
}

func (s *scriptedLauncher) Ensure(_ context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensures++
	return s.running[spec.AgentID], s.err
}

func (s *scriptedLauncher) Lookup(_ context.Context, spec launcher.Spec) (launcher.Endpoint, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ep, ok := s.running[spec.AgentID]
	return ep, ok, nil
}
func (s *scriptedLauncher) Stop(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, id)
	return s.stopErr
}
func (s *scriptedLauncher) Remove(context.Context, string) error { return nil }
func (s *scriptedLauncher) Running() map[string]launcher.Endpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
func (s *scriptedLauncher) Kind() string { return "agent-sandbox (warm pool pdp-runtimes)" }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWakeAndHibernationMetrics(t *testing.T) {
	m := metrics.New(metrics.Info{})
	l := &scriptedLauncher{running: map[string]launcher.Endpoint{"a1": {BaseURL: "http://127.0.0.1:1", Token: "t"}}}
	mgr := New(l, time.Millisecond, quiet())
	mgr.Metrics = m
	ctx := context.Background()

	if _, err := mgr.Wake(ctx, launcher.Spec{AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	l.err = errors.New("boom with a secret url http://user:pw@host/")
	if _, err := mgr.Wake(ctx, launcher.Spec{AgentID: "a1"}); err == nil {
		t.Fatal("no error")
	}
	l.err = context.DeadlineExceeded
	_, _ = mgr.Wake(ctx, launcher.Spec{AgentID: "a1"})
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	l.err = context.Canceled
	_, _ = mgr.Wake(cctx, launcher.Spec{AgentID: "a1"})

	_ = mgr.Hibernate(ctx, "a1")
	l.stopErr = errors.New("nope")
	_ = mgr.Hibernate(ctx, "a1")

	s := metricstest.Gather(t, m)
	for _, c := range []struct {
		kv   []string
		want float64
	}{
		{[]string{"launcher", "agent-sandbox", "start", "running", "result", "ok"}, 1},
		{[]string{"start", "unknown", "result", "error"}, 1},
		{[]string{"result", "timeout"}, 1},
		{[]string{"result", "canceled"}, 1},
	} {
		if got := s.Sum("pdp_agent_wakes_total", c.kv...); got != c.want {
			t.Errorf("wakes%v = %v, want %v", c.kv, got, c.want)
		}
	}
	if got := s.Sum("pdp_agent_hibernations_total", "reason", "request", "result", "ok"); got != 1 {
		t.Errorf("request hibernations ok = %v", got)
	}
	if got := s.Sum("pdp_agent_hibernations_total", "reason", "request", "result", "error"); got != 1 {
		t.Errorf("request hibernations failed = %v", got)
	}
}

func TestReaperMetrics(t *testing.T) {
	m := metrics.New(metrics.Info{})
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"busyThreads":0,"lastActivityAt":0}}`)
	}))
	defer runtime.Close()
	l := &scriptedLauncher{running: map[string]launcher.Endpoint{
		"a1": {BaseURL: runtime.URL, Token: "t"}, "a2": {BaseURL: runtime.URL, Token: "t"},
	}}
	mgr := New(l, time.Millisecond, quiet())
	mgr.Metrics = m
	mgr.ReapOnce(context.Background())

	if len(l.stopped) != 2 {
		t.Fatalf("stopped %v", l.stopped)
	}
	s := metricstest.Gather(t, m)
	if got := s.Sum("pdp_agents_running"); got != 2 {
		t.Errorf("agents running = %v", got)
	}
	if got := s.Sum("pdp_agent_hibernations_total", "reason", "idle", "result", "ok"); got != 2 {
		t.Errorf("idle hibernations = %v", got)
	}
}

var traceparent = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-0[01]$`)

// A relayed request has a wake span and a runtime span, and the runtime
// gets the runtime span's trace context; the spans hold no token or path.
func TestProxyTracesTheRuntimeRequest(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	var got http.Header
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer runtime.Close()
	mgr := New(fixedLauncher{launcher.Endpoint{BaseURL: runtime.URL, Token: "runtime-secret-token"}}, time.Minute, quiet())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/ignored?x=1", nil)
	req.Header.Set("Cookie", "session=secret")
	mgr.Proxy(rec, req, launcher.Spec{AgentID: "a1", Token: "runtime-secret-token"}, "/threads/t1/messages")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if !traceparent.MatchString(got.Get("Traceparent")) {
		t.Errorf("the runtime got traceparent %q", got.Get("Traceparent"))
	}
	if got.Get("Cookie") != "" {
		t.Error("the caller's cookie reached the runtime")
	}
	spans := map[string]tracetest.SpanStub{}
	for _, s := range exp.GetSpans() {
		spans[s.Name] = s
	}
	wake, rt := spans["agent.wake"], spans["agent.runtime_request"]
	if wake.Name == "" || rt.Name == "" {
		t.Fatalf("spans: %v", exp.GetSpans())
	}
	if got.Get("Traceparent")[3:35] != rt.SpanContext.TraceID().String() || got.Get("Traceparent")[36:52] != rt.SpanContext.SpanID().String() {
		t.Errorf("the runtime's traceparent %q is not the runtime span's %s", got.Get("Traceparent"), rt.SpanContext.SpanID())
	}
	for _, s := range exp.GetSpans() {
		for _, a := range s.Attributes {
			if v := a.Value.Emit(); v == "runtime-secret-token" || v == "/threads/t1/messages" || v == "session=secret" {
				t.Errorf("span %s holds %s=%q", s.Name, a.Key, v)
			}
		}
	}
}

// lockerStub is a Locker for one process: it records what was locked and can refuse the non-blocking lock.
type lockerStub struct {
	mu     sync.Mutex
	locked []string
	refuse bool
}

func (l *lockerStub) Lock(_ context.Context, key string) (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.locked = append(l.locked, key)
	return func() {}, nil
}

func (l *lockerStub) TryLock(_ context.Context, key string) (func(), bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refuse {
		return nil, false, nil
	}
	l.locked = append(l.locked, key)
	return func() {}, true, nil
}

// With several replicas an agent that is already running is found by one Lookup: no lock is taken, nothing is started,
// and the wake counts as "running".
func TestWakeThroughTheLockMetrics(t *testing.T) {
	m := metrics.New(metrics.Info{})
	l := &scriptedLauncher{running: map[string]launcher.Endpoint{"a1": {BaseURL: "http://127.0.0.1:1", Token: "t"}}}
	lk := &lockerStub{}
	mgr := New(l, time.Millisecond, quiet())
	mgr.Metrics = m
	mgr.SetLocker(lk)

	if _, err := mgr.Wake(context.Background(), launcher.Spec{AgentID: "a1"}); err != nil {
		t.Fatal(err)
	}
	if l.ensures != 0 || len(lk.locked) != 0 {
		t.Errorf("a running agent started %d times under locks %v", l.ensures, lk.locked)
	}
	// Not running: the start is serialised by the agent's lock, then Ensure runs once.
	if _, err := mgr.Wake(context.Background(), launcher.Spec{AgentID: "a2"}); err != nil {
		t.Fatal(err)
	}
	if l.ensures != 1 || len(lk.locked) != 1 || lk.locked[0] != wakeLockPrefix+"a2" {
		t.Errorf("ensures %d, locks %v", l.ensures, lk.locked)
	}
	if got := metricstest.Gather(t, m).Sum("pdp_agent_wakes_total", "result", "ok"); got != 2 {
		t.Errorf("wakes = %v, want 2", got)
	}
}
