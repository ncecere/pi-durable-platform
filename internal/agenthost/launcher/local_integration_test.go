package launcher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/agenthost/provision"
)

// TestAgentLifecycle provisions an agent's storage, runs its real runtime,
// chats, hibernates it, wakes it on another process, and deletes it.
// Needs PDP_TEST_DATABASE_URL (a server where it may create a database) and
// the runtime's node_modules (PDP_RUNTIME_DIR, default ../../../runtime).
func TestAgentLifecycle(t *testing.T) {
	adminURL := os.Getenv("PDP_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PDP_TEST_DATABASE_URL not set")
	}
	runtimeDir := os.Getenv("PDP_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir, _ = filepath.Abs("../../../runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	agentsURL := createDatabase(t, ctx, adminURL)
	pool, err := pgxpool.New(ctx, agentsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	prov := provision.New(pool)
	if err := prov.Prepare(ctx); err != nil {
		t.Fatal(err)
	}

	agentID := "0d9a8c2e-1111-4a5b-9c3d-abcdef012345"
	creds, err := prov.Create(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	dsn, _ := creds.DSN(agentsURL, "")
	spec := launcher.Spec{AgentID: agentID, Name: "Test", Schema: creds.Schema, DatabaseURL: dsn, Token: "test-token-123",
		Env: map[string]string{"MODEL_PROVIDER": "demo", "DEMO_TOKENS_PER_SECOND": "500"}}

	l := launcher.NewLocal(runtimeDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer l.StopAll(context.Background())

	ep, err := l.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	// Concurrent Ensure returns the same runtime.
	if ep2, err := l.Ensure(ctx, spec); err != nil || ep2.BaseURL != ep.BaseURL {
		t.Fatalf("second Ensure: %v %v", ep2, err)
	}
	post(t, ep, "/threads/t1/messages", `{"text":"hello","requestId":"m1"}`)
	waitIdle(t, ep, "t1")

	// Hibernate, then wake: a new process, same conversation.
	if err := l.Stop(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	if len(l.Running()) != 0 {
		t.Fatal("agent still running after Stop")
	}
	ep, err = l.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	post(t, ep, "/threads/t1/messages", `{"text":"again","requestId":"m2"}`)
	view := waitIdle(t, ep, "t1")
	if !strings.Contains(view, "message 2 in this chat") {
		t.Fatalf("conversation not resumed after wake: %s", view)
	}

	// The runtime rejects requests without its token.
	resp, err := http.Get(ep.BaseURL + "/threads")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: %d", resp.StatusCode)
	}

	// The reaper hibernates it once idle.
	mgr := agenthost.New(l, 300*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.ReapOnce(ctx) // too recent: the runtime just did work
	if !mgr.Running(agentID) {
		t.Fatal("reaper stopped an agent that was just active")
	}
	time.Sleep(400 * time.Millisecond)
	mgr.ReapOnce(ctx)
	if mgr.Running(agentID) {
		t.Fatal("reaper did not hibernate the idle agent")
	}
	if err := prov.Delete(ctx, agentID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_namespace WHERE nspname = $1", creds.Schema).Scan(&n); err != nil || n != 0 {
		t.Fatalf("schema not removed: %d %v", n, err)
	}
}

func post(t *testing.T, ep launcher.Endpoint, path, body string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ep.BaseURL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s: %d %s", path, resp.StatusCode, b)
	}
}

func waitIdle(t *testing.T, ep launcher.Endpoint, thread string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, ep.BaseURL+"/threads/"+thread, nil)
		req.Header.Set("Authorization", "Bearer "+ep.Token)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			var body struct {
				Data struct {
					Busy     bool              `json:"busy"`
					Messages []json.RawMessage `json:"messages"`
				} `json:"data"`
			}
			v := &body.Data
			if json.Unmarshal(b, &body) == nil && !v.Busy && len(v.Messages) > 0 {
				return string(b)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("thread did not become idle")
	return ""
}

func createDatabase(t *testing.T, ctx context.Context, adminURL string) string {
	t.Helper()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("pdp_agents_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), adminURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
