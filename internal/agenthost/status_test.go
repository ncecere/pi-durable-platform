package agenthost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
)

func statusManager(url string) *Manager {
	return New(fixedLauncher{launcher.Endpoint{BaseURL: url, Token: "tok"}}, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A runtime is not trusted: one that streams an endless /status body must get
// an error after the limit, without the platform buffering it.
func TestStatusRefusesAnEndlessBody(t *testing.T) {
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"busyThreads":0,"pad":"`)
		chunk := []byte(strings.Repeat("a", 1<<20))
		for r.Context().Err() == nil {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer rt.Close()
	m := statusManager(rt.URL)
	ep := launcher.Endpoint{BaseURL: rt.URL, Token: "tok"}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	for range 5 {
		if _, err := m.status(context.Background(), ep); err == nil {
			t.Fatal("an endless status body was accepted")
		}
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("refusing the body took %v", d)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 8<<20 {
		t.Fatalf("heap grew by %d bytes", grown)
	}
}

// A body over the limit is an error even when its start is a complete document.
func TestStatusOverTheLimitIsNotATruncatedParse(t *testing.T) {
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"busyThreads":3}}`+strings.Repeat(" ", maxStatusBytes))
	}))
	defer rt.Close()
	if _, err := statusManager(rt.URL).status(context.Background(), launcher.Endpoint{BaseURL: rt.URL, Token: "tok"}); err == nil {
		t.Fatal("an oversized status reply was accepted")
	}
}

func TestStatusReadsAnOrdinaryReply(t *testing.T) {
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"busyThreads":2,"openStreams":1,"lastActivityAt":42}}`)
	}))
	defer rt.Close()
	st, err := statusManager(rt.URL).status(context.Background(), launcher.Endpoint{BaseURL: rt.URL, Token: "tok"})
	if err != nil || st.BusyThreads != 2 || st.OpenStreams == nil || *st.OpenStreams != 1 || st.LastActivityAt != 42 {
		t.Fatalf("status = %+v, %v", st, err)
	}
}

// The runtime reports its contract and pi-durable versions (contract version
// 1). Nothing is stored yet; what is out of bounds or of another type is
// dropped without failing the report (the reaper still reads the activity),
// and a runtime that predates the fields still decodes.
func TestStatusReadsBoundedVersions(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		contract int // 0: none
		pi       string
	}{
		{"current", `{"data":{"busyThreads":0,"contractVersion":1,"piDurableVersion":"1.3.0"}}`, 1, "1.3.0"},
		{"pre-release", `{"data":{"contractVersion":2,"piDurableVersion":"1.4.0-rc.1"}}`, 2, "1.4.0-rc.1"},
		{"older runtime", `{"data":{"busyThreads":1}}`, 0, ""},
		{"contract version zero", `{"data":{"contractVersion":0}}`, 0, ""},
		{"contract version negative", `{"data":{"contractVersion":-4}}`, 0, ""},
		{"contract version too large", `{"data":{"contractVersion":1000000}}`, 0, ""},
		{"not a version", `{"data":{"piDurableVersion":"latest"}}`, 0, ""},
		{"markup", `{"data":{"piDurableVersion":"1.3.0<script>"}}`, 0, ""},
		{"too long", `{"data":{"piDurableVersion":"1.3.0-` + strings.Repeat("a", 200) + `"}}`, 0, ""},
		{"wrong types", `{"data":{"busyThreads":2,"contractVersion":"one","piDurableVersion":13}}`, 0, ""},
		{"fractional contract version", `{"data":{"contractVersion":1.5}}`, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer rt.Close()
			st, err := statusManager(rt.URL).status(context.Background(), launcher.Endpoint{BaseURL: rt.URL, Token: "tok"})
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			if st.ContractVersion != nil {
				got = *st.ContractVersion
			}
			if got != tc.contract || st.PiDurableVersion != tc.pi {
				t.Fatalf("contract %d, pi-durable %q; want %d, %q", got, st.PiDurableVersion, tc.contract, tc.pi)
			}
		})
	}
	// A malformed activity field is still an error.
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"busyThreads":"two","contractVersion":1}}`)
	}))
	defer rt.Close()
	if _, err := statusManager(rt.URL).status(context.Background(), launcher.Endpoint{BaseURL: rt.URL, Token: "tok"}); err == nil {
		t.Fatal("a string busyThreads was accepted")
	}
}

// Contract version 2's backgroundWork is read bounded: whole counts from 0
// (clamped to 1000), a plausible start time whenever there is work; anything
// else drops the field (the reaper then judges the runtime as before), never
// the report.
func TestStatusReadsBoundedBackgroundWork(t *testing.T) {
	const at = 1760000000000
	cases := []struct {
		name           string
		field          string // backgroundWork's JSON ("": absent)
		ok             bool
		tasks, waiting int
		oldest         int64
	}{
		{"absent", "", false, 0, 0, 0},
		{"none", `{"tasks":0,"oldestStartedAt":null,"waitingForPerson":0}`, true, 0, 0, 0},
		{"some", `{"tasks":2,"oldestStartedAt":1760000000000,"waitingForPerson":1}`, true, 2, 1, at},
		{"without waitingForPerson", `{"tasks":1,"oldestStartedAt":1760000000000}`, true, 1, 0, at},
		{"clamped", `{"tasks":99999999,"oldestStartedAt":1760000000000,"waitingForPerson":5000}`, true, 1000, 1000, at},
		{"a start time of another type", `{"tasks":0,"oldestStartedAt":"soon"}`, false, 0, 0, 0},
		{"null", `null`, false, 0, 0, 0},
		{"an array", `[1]`, false, 0, 0, 0},
		{"no tasks", `{"oldestStartedAt":1760000000000}`, false, 0, 0, 0},
		{"negative tasks", `{"tasks":-1}`, false, 0, 0, 0},
		{"fractional tasks", `{"tasks":0.5}`, false, 0, 0, 0},
		{"tasks as a string", `{"tasks":"2","oldestStartedAt":1760000000000}`, false, 0, 0, 0},
		{"huge tasks", `{"tasks":1e30,"oldestStartedAt":1760000000000}`, false, 0, 0, 0},
		{"work without a start", `{"tasks":3}`, false, 0, 0, 0},
		{"a start in seconds", `{"tasks":3,"oldestStartedAt":1760000000}`, false, 0, 0, 0},
		{"a start in the year 5000", `{"tasks":3,"oldestStartedAt":95617584000000}`, false, 0, 0, 0},
		{"negative waiting", `{"tasks":0,"waitingForPerson":-2}`, false, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"data":{"busyThreads":1,"openStreams":0,"lastActivityAt":42`
			if tc.field != "" {
				body += `,"backgroundWork":` + tc.field
			}
			body += `}}`
			var wrapped struct{ Data runtimeStatus }
			if err := json.Unmarshal([]byte(body), &wrapped); err != nil {
				t.Fatalf("the report was refused: %v", err)
			}
			st, bg := wrapped.Data, wrapped.Data.Background
			if st.BusyThreads != 1 || st.LastActivityAt != 42 {
				t.Fatalf("activity fields: %+v", st)
			}
			if (bg != nil) != tc.ok {
				t.Fatalf("background = %+v, want read %v", bg, tc.ok)
			}
			if bg == nil {
				return
			}
			var oldest int64
			if !bg.OldestStartedAt.IsZero() {
				oldest = bg.OldestStartedAt.UnixMilli()
			}
			if bg.Tasks != tc.tasks || bg.WaitingForPerson != tc.waiting || oldest != tc.oldest {
				t.Fatalf("background = %+v (oldest %d)", bg, oldest)
			}
		})
	}
}

// While background work keeps an agent awake, the reaper asks again after
// half an idle timeout, or when the oldest work reaches the ceiling if that
// comes sooner.
func TestRecheckWaitsNoLongerThanTheCeiling(t *testing.T) {
	m := statusManager("http://unused")
	m.idle, m.BackgroundMax = 10*time.Minute, time.Hour
	now := time.Now()
	st := runtimeStatus{LastActivityAt: now.Add(-time.Hour).UnixMilli(), Background: &backgroundWork{Tasks: 1, OldestStartedAt: now.Add(-59 * time.Minute)}}
	reasons, over := m.judge(st, 0, now)
	if reasons != awakeBackground || over {
		t.Fatalf("reasons %v, over %v", reasons.names(), over)
	}
	if wait := m.recheckIn(st, reasons, now); wait != time.Minute {
		t.Fatalf("wait %v, want 1m (the ceiling)", wait)
	}
	st.Background.OldestStartedAt = now.Add(-time.Minute)
	if wait := m.recheckIn(st, reasons, now); wait != 5*time.Minute {
		t.Fatalf("wait %v, want 5m (half the idle timeout)", wait)
	}
}
