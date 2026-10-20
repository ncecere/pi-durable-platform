package agenthost

import (
	"context"
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
