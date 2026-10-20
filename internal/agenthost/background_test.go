package agenthost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
)

// bgRuntime answers /status with what the test sets and POST
// /background/abort as the library does: it aborts the units older than the
// age (here: all of them, when the oldest is) and reports none afterwards.
type bgRuntime struct {
	srv        *httptest.Server
	mu         sync.Mutex
	busy       int
	lastActive time.Time
	// background is the backgroundWork object (nil: a runtime before
	// contract version 2, which doesn't send it).
	background map[string]any
	// raw replaces the whole backgroundWork value when set.
	raw     string
	aborts  []int64 // olderThanMs of each abort request
	younger bool    // after an abort, younger work is left
}

func newBGRuntime(t *testing.T) *bgRuntime {
	t.Helper()
	r := &bgRuntime{lastActive: time.Now().Add(-time.Hour)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case req.Method == http.MethodGet && req.URL.Path == "/status":
			body := map[string]any{"busyThreads": r.busy, "openStreams": 0, "lastActivityAt": r.lastActive.UnixMilli()}
			if r.background != nil {
				body["backgroundWork"] = r.background
			}
			out, _ := json.Marshal(map[string]any{"data": body})
			if r.raw != "" {
				out = []byte(strings.Replace(string(out), `"busyThreads"`, `"backgroundWork":`+r.raw+`,"busyThreads"`, 1))
			}
			_, _ = w.Write(out)
		case req.Method == http.MethodPost && req.URL.Path == "/background/abort":
			var in struct {
				OlderThanMs int64 `json:"olderThanMs"`
			}
			if json.NewDecoder(req.Body).Decode(&in) != nil {
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			r.aborts = append(r.aborts, in.OlderThanMs)
			aborted := 0
			if r.background != nil && r.background["tasks"].(int) > 0 {
				aborted = r.background["tasks"].(int)
				r.background = map[string]any{"tasks": 0, "oldestStartedAt": nil, "waitingForPerson": 0}
				if r.younger {
					r.background = map[string]any{"tasks": 1, "oldestStartedAt": time.Now().Add(-time.Minute).UnixMilli(), "waitingForPerson": 0}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"aborted": aborted, "settled": true}})
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *bgRuntime) set(f func(r *bgRuntime)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

func (r *bgRuntime) abortCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.aborts)
}

func background(tasks int, startedAgo time.Duration, waitingForPerson int) map[string]any {
	bg := map[string]any{"tasks": tasks, "oldestStartedAt": nil, "waitingForPerson": waitingForPerson}
	if tasks > 0 {
		bg["oldestStartedAt"] = time.Now().Add(-startedAgo).UnixMilli()
	}
	return bg
}

type bgEnv struct {
	rt      *bgRuntime
	l       *scriptedLauncher
	m       *Manager
	met     *metrics.Metrics
	audited []BackgroundAbort
}

// newBGEnv is one running agent (a1) on a fake runtime, with a one-minute
// idle timeout and a one-hour ceiling.
func newBGEnv(t *testing.T) *bgEnv {
	t.Helper()
	e := &bgEnv{rt: newBGRuntime(t), met: metrics.New(metrics.Info{})}
	e.l = &scriptedLauncher{running: map[string]launcher.Endpoint{"a1": {BaseURL: e.rt.srv.URL, Token: "tok"}}}
	e.m = New(e.l, time.Minute, quiet())
	e.m.Metrics, e.m.BackgroundMax = e.met, time.Hour
	e.m.BackgroundAborted = func(_ context.Context, agentID string, a BackgroundAbort) error {
		if agentID != "a1" {
			t.Errorf("audited agent %q", agentID)
		}
		e.audited = append(e.audited, a)
		return nil
	}
	return e
}

// reap runs a pass as if the hints of the last one had expired (the time to
// ask the runtime again), and returns the stops so far.
func (e *bgEnv) reap() int {
	e.m.mu.Lock()
	clear(e.m.skipUntil)
	e.m.mu.Unlock()
	e.m.ReapOnce(context.Background())
	e.l.mu.Lock()
	defer e.l.mu.Unlock()
	return len(e.l.stopped)
}

// An agent with background work running is not hibernated until the work
// ends: then the idle clock starts (the runtime touches its activity at the
// end), and it hibernates after the timeout.
func TestReaperKeepsAnAgentWithBackgroundWorkAwake(t *testing.T) {
	e := newBGEnv(t)
	e.rt.set(func(r *bgRuntime) { r.background = background(2, 10*time.Minute, 0) })
	for range 3 {
		if n := e.reap(); n != 0 {
			t.Fatalf("an agent with background work was hibernated (%d stops)", n)
		}
	}
	s := metricstest.Gather(t, e.met)
	if s.Sum("pdp_agents_awake", "reason", "background") != 1 || s.Sum("pdp_agents_awake", "reason", "recent") != 0 {
		t.Fatalf("agents_awake: background %v, recent %v", s.Sum("pdp_agents_awake", "reason", "background"), s.Sum("pdp_agents_awake", "reason", "recent"))
	}
	// The work ends, and the runtime counts its end as activity.
	e.rt.set(func(r *bgRuntime) { r.background, r.lastActive = background(0, 0, 0), time.Now() })
	if n := e.reap(); n != 0 {
		t.Fatal("hibernated right after its background work ended")
	}
	e.rt.set(func(r *bgRuntime) { r.lastActive = time.Now().Add(-2 * time.Minute) })
	if n := e.reap(); n != 1 {
		t.Fatalf("stops = %d after the idle timeout", n)
	}
	if e.rt.abortCount() != 0 || len(e.audited) != 0 {
		t.Fatalf("work under the ceiling was aborted: %d aborts", e.rt.abortCount())
	}
	if got := metricstest.Gather(t, e.met).Sum("pdp_agents_awake", "reason", "background"); got != 0 {
		t.Fatalf("a hibernated agent still counts as awake: %v", got)
	}
}

// Past the ceiling, background work no longer keeps the agent awake: the
// reaper has the runtime abort it (with the ceiling as the age), audits and
// counts it, and hibernates the agent.
func TestReaperAbortsBackgroundWorkPastTheCeilingAndHibernates(t *testing.T) {
	e := newBGEnv(t)
	e.rt.set(func(r *bgRuntime) { r.background = background(3, 2*time.Hour, 0) })
	if n := e.reap(); n != 1 {
		t.Fatalf("stops = %d, want 1", n)
	}
	e.rt.mu.Lock()
	aborts := append([]int64(nil), e.rt.aborts...)
	e.rt.mu.Unlock()
	if len(aborts) != 1 || aborts[0] != time.Hour.Milliseconds() {
		t.Fatalf("abort requests %v, want one with olderThanMs %d", aborts, time.Hour.Milliseconds())
	}
	if len(e.audited) != 1 || e.audited[0].Aborted != 3 || !e.audited[0].Settled || e.audited[0].Ceiling != time.Hour ||
		time.Since(e.audited[0].OldestStartedAt) < 2*time.Hour-time.Minute {
		t.Fatalf("audited %+v", e.audited)
	}
	if got := metricstest.Gather(t, e.met).Sum("pdp_agent_background_aborts_total", "result", "ok"); got != 1 {
		t.Fatalf("background aborts = %v", got)
	}
}

// The ceiling never cuts into a person's reply or a watched thread: work past
// it is aborted only when nothing else keeps the agent awake, and younger
// background work left after the abort keeps it awake.
func TestReaperAbortsOnlyWhenTheCeilingIsAllThatKeepsTheAgentAwake(t *testing.T) {
	e := newBGEnv(t)
	e.rt.set(func(r *bgRuntime) { r.background, r.busy = background(1, 2*time.Hour, 0), 1 })
	if n := e.reap(); n != 0 || e.rt.abortCount() != 0 {
		t.Fatalf("a replying agent: stops %d, aborts %d", n, e.rt.abortCount())
	}
	e.rt.set(func(r *bgRuntime) { r.busy, r.lastActive = 0, time.Now() })
	if n := e.reap(); n != 0 || e.rt.abortCount() != 0 {
		t.Fatalf("a recently used agent: stops %d, aborts %d", n, e.rt.abortCount())
	}
	// Idle but for the old work, and younger work besides.
	e.rt.set(func(r *bgRuntime) { r.lastActive, r.younger = time.Now().Add(-time.Hour), true })
	if n := e.reap(); n != 0 || e.rt.abortCount() != 1 {
		t.Fatalf("with younger work left: stops %d, aborts %d", n, e.rt.abortCount())
	}
}

// A task waiting for a person keeps nothing awake: the runtime counts it as
// waitingForPerson only, and the agent hibernates, without an abort.
func TestReaperHibernatesAnAgentWaitingForAPerson(t *testing.T) {
	e := newBGEnv(t)
	e.rt.set(func(r *bgRuntime) { r.background = background(0, 0, 2) })
	if n := e.reap(); n != 1 || e.rt.abortCount() != 0 {
		t.Fatalf("stops %d, aborts %d", n, e.rt.abortCount())
	}
}

// A runtime that reports no background work (before contract version 2), or
// a malformed report, is judged by the version 1 rule.
func TestReaperJudgesARuntimeWithoutBackgroundWorkAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"not reported", ""},
		{"null", "null"},
		{"not an object", `"busy"`},
		{"negative", `{"tasks":-1}`},
		{"fractional", `{"tasks":1.5,"oldestStartedAt":1760000000000}`},
		{"no start time", `{"tasks":2}`},
		{"start time in seconds", `{"tasks":2,"oldestStartedAt":1760000000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBGEnv(t)
			e.rt.set(func(r *bgRuntime) { r.raw = tc.raw })
			if n := e.reap(); n != 1 || e.rt.abortCount() != 0 {
				t.Fatalf("stops %d, aborts %d", n, e.rt.abortCount())
			}
		})
	}
	// And at work, it stays awake as before.
	e := newBGEnv(t)
	e.rt.set(func(r *bgRuntime) { r.busy = 1 })
	if n := e.reap(); n != 0 {
		t.Fatal("a busy runtime without background work was hibernated")
	}
}

// A ceiling abort that fails is counted and logged; the work past the ceiling
// no longer keeps the agent awake, so it hibernates anyway (the work resumes,
// still old, if it wakes).
func TestReaperHibernatesWhenTheAbortFails(t *testing.T) {
	e := newBGEnv(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_, _ = io.WriteString(w, `{"data":{"busyThreads":0,"openStreams":0,"lastActivityAt":1,"backgroundWork":{"tasks":1,"oldestStartedAt":`+
				jsonInt(time.Now().Add(-3*time.Hour).UnixMilli())+`,"waitingForPerson":0}}}`)
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	e.l.running["a1"] = launcher.Endpoint{BaseURL: broken.URL, Token: "tok"}
	if n := e.reap(); n != 1 {
		t.Fatalf("stops = %d", n)
	}
	if got := metricstest.Gather(t, e.met).Sum("pdp_agent_background_aborts_total", "result", "error"); got != 1 || len(e.audited) != 0 {
		t.Fatalf("failed aborts = %v, audited %d", got, len(e.audited))
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

// Awake says why a running agent isn't hibernating, from its runtime's
// report, and nothing for one that isn't running.
func TestAwakeReportsWhyAnAgentIsAwake(t *testing.T) {
	e := newBGEnv(t)
	started := time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)
	e.rt.set(func(r *bgRuntime) {
		r.busy, r.background = 1, map[string]any{"tasks": 2, "oldestStartedAt": started.UnixMilli(), "waitingForPerson": 1}
	})
	got := e.m.Awake(context.Background(), "a1")
	if got == nil || strings.Join(got.Reasons, ",") != "reply,background" || got.BackgroundTasks != 2 || got.WaitingForPerson != 1 ||
		got.BackgroundSince == nil || !got.BackgroundSince.Equal(started) || got.OverCeiling {
		t.Fatalf("awake = %+v", got)
	}
	if e.m.Awake(context.Background(), "nobody") != nil {
		t.Fatal("an agent that isn't running is awake")
	}
}
