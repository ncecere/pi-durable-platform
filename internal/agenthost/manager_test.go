package agenthost

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
)

// fixedLauncher always returns one endpoint.
type fixedLauncher struct{ ep launcher.Endpoint }

func (f fixedLauncher) Ensure(context.Context, launcher.Spec) (launcher.Endpoint, error) {
	return f.ep, nil
}
func (f fixedLauncher) Stop(context.Context, string) error    { return nil }
func (f fixedLauncher) Running() map[string]launcher.Endpoint { return nil }
func (f fixedLauncher) Kind() string                          { return "fixed" }

// Shutdown ends open event streams at once (and closing twice is harmless).
func TestCloseStreamsEndsProxiedStreams(t *testing.T) {
	runtime := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: view\ndata: {}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer runtime.Close()
	m := New(fixedLauncher{launcher.Endpoint{BaseURL: runtime.URL, Token: "tok"}}, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.Proxy(w, r, launcher.Spec{AgentID: "a1", Token: "tok"}, "/threads/t1/events")
	}))
	defer platform.Close()

	resp, err := http.Get(platform.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	if n, _ := resp.Body.Read(buf); !strings.HasPrefix(string(buf[:n]), "event: view") {
		t.Fatalf("first event = %q", buf[:n])
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, resp.Body); close(done) }()
	m.CloseStreams()
	m.CloseStreams()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open after CloseStreams")
	}
}
