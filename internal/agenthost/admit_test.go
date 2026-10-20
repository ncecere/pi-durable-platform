package agenthost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
)

// startingLauncher starts (Ensure) whatever endpoint it has, reports it
// running only once started, and records stops.
type startingLauncher struct {
	ep      launcher.Endpoint
	mu      sync.Mutex
	running bool
	stops   int
}

func (l *startingLauncher) Ensure(context.Context, launcher.Spec) (launcher.Endpoint, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running = true
	return l.ep, nil
}
func (l *startingLauncher) Lookup(context.Context, launcher.Spec) (launcher.Endpoint, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ep, l.running, nil
}
func (l *startingLauncher) Stop(context.Context, string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.running = false
	l.stops++
	return nil
}
func (l *startingLauncher) Running() map[string]launcher.Endpoint { return nil }
func (l *startingLauncher) Remove(context.Context, string) error  { return nil }
func (l *startingLauncher) Kind() string                          { return "starting" }

// After a start the manager asks Admit with the version the runtime reports
// at /status: a refused runtime is stopped and the request answered 409
// runtime_too_old with the reason; an admitted one is relayed to, and a
// running one is not asked again.
func TestAdmitAfterStart(t *testing.T) {
	version := "1.3.0"
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"busyThreads": 0, "openStreams": 0, "lastActivityAt": 1, "piDurableVersion": version}})
			return
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	}))
	defer runtime.Close()
	l := &startingLauncher{ep: launcher.Endpoint{BaseURL: runtime.URL, Token: "tok"}}
	m := New(l, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var asked []string
	m.Admit = func(_ context.Context, agentID, v string) error {
		asked = append(asked, agentID+"@"+v)
		if v < "2" {
			return &RefusedError{Message: "too old: " + v}
		}
		return nil
	}
	call := func() (int, string) {
		rec := httptest.NewRecorder()
		m.Proxy(rec, httptest.NewRequest("GET", "/v1/agents/a1/threads/t1", nil), launcher.Spec{AgentID: "a1", Token: "tok"}, "/threads/t1")
		return rec.Code, rec.Body.String()
	}

	code, body := call()
	if code != http.StatusConflict || !json.Valid([]byte(body)) || body != `{"error":{"code":"runtime_too_old","message":"too old: 1.3.0"}}`+"\n" {
		t.Fatalf("refused: %d %s", code, body)
	}
	if l.stops != 1 || l.running {
		t.Fatalf("the refused runtime was not stopped: %d stops", l.stops)
	}

	version = "2.0.0"
	if code, body := call(); code != http.StatusOK {
		t.Fatalf("admitted: %d %s", code, body)
	}
	if code, _ := call(); code != http.StatusOK {
		t.Fatalf("running: %d", code)
	}
	if len(asked) != 2 || asked[0] != "a1@1.3.0" || asked[1] != "a1@2.0.0" {
		t.Fatalf("Admit asked %v", asked)
	}
}
