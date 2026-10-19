package metrics_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	dto "github.com/prometheus/client_model/go"

	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
)

func newMetrics() *metrics.Metrics {
	return metrics.New(metrics.Info{Version: "1.2.3", Commit: "abc123"})
}

// The metrics listener serves /metrics and nothing else.
func TestHandlerServesOnlyMetrics(t *testing.T) {
	srv := httptest.NewServer(newMetrics().Handler())
	defer srv.Close()
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/metrics", 200},
		{"GET", "/", 404},
		{"GET", "/healthz", 404},
		{"GET", "/v1/me", 404},
		{"GET", "/metrics/extra", 404},
		{"POST", "/metrics", 405},
	} {
		req, _ := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, res.StatusCode, tc.want)
		}
	}
}

// Build info, the Go runtime and process collectors are there.
func TestBuildInfoAndRuntimeCollectors(t *testing.T) {
	s := metricstest.Gather(t, newMetrics())
	if !s.Has("pdp_build_info", "version", "1.2.3", "commit", "abc123") || s.Sum("pdp_build_info") != 1 {
		t.Errorf("pdp_build_info missing:\n%s", s.Text)
	}
	for _, name := range []string{"go_goroutines", "go_memstats_alloc_bytes", "process_cpu_seconds_total"} {
		if _, ok := s.Samples[name]; !ok {
			t.Errorf("%s is not exported", name)
		}
	}
}

// A package works with nil metrics (tests, commands that don't serve).
func TestNilMetricsDoNothing(t *testing.T) {
	var m *metrics.Metrics
	m.HTTPRequest("api", "GET", "GET /x", 200, time.Second)
	m.InFlight("api", 1)
	m.Wake("local", "cold", "ok", time.Second)
	m.SetAgentsRunning(1)
	m.SetAgents(1, 1)
	m.Hibernation("idle", nil)
	m.ModelRequest("openai", "gpt", 200, time.Second)
	m.ModelTokens("openai", "gpt", 1, 1, 1, 1)
	m.MCPRequest("x", 200, time.Second)
	m.MCPToolCalls("x", "ok", 1)
	m.OAuthTokenIssued("refresh_token")
	m.OAuthTokenError("invalid_grant")
	m.OAuthRefreshReuse()
	m.SignIn("oidc", "success", "")
	m.RateLimited("login:abc")
	m.ThreadSubscribers(1)
	m.Loop("reaper")(nil)
	m.RegisterPool("platform", nil)
	if m.Registry() != nil {
		t.Error("a nil Metrics has a registry")
	}
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	if res, err := http.Get(srv.URL + "/metrics"); err != nil || res.StatusCode != 404 {
		t.Errorf("nil handler: %v %v", res, err)
	}
}

func TestLabelHelpers(t *testing.T) {
	for in, want := range map[int]string{100: "1xx", 200: "2xx", 204: "2xx", 302: "3xx", 404: "4xx", 499: "4xx", 503: "5xx", 0: "other", 700: "other"} {
		if got := metrics.StatusClass(in); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"GET": "GET", "DELETE": "DELETE", "PROPFIND": "other", "get": "other"} {
		if got := metrics.Method(in); got != want {
			t.Errorf("Method(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"GET /v1/teams/{team}":                     "/v1/teams/{team}",
		"POST /model-proxy/{providerId}/{path...}": "/model-proxy/{providerId}/{path...}",
		"/mcp-proxy/server/{id}":                   "/mcp-proxy/server/{id}",
		"/":                                        "unmatched",
		"":                                         "unmatched",
	} {
		if got := metrics.RoutePattern(in); got != want {
			t.Errorf("RoutePattern(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"local": "local", "kubernetes": "kubernetes", "agent-sandbox (warm pool pdp-runtimes)": "agent-sandbox", "": "unknown",
	} {
		if got := metrics.LauncherMode(in); got != want {
			t.Errorf("LauncherMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// Whatever is thrown at the labels, the series stay bounded: paths and IDs
// never reach a label, and the labels an administrator chooses are capped.
func TestNoCardinalityLeaks(t *testing.T) {
	m := newMetrics()
	for i := 0; i < 5000; i++ {
		id := fmt.Sprintf("0b9a1c2e-%04d-4c1d-9a6e-%012d", i%10000, i)
		// A pattern is what the mux matched (bounded by the code); a path
		// never is one. Even a flood of distinct patterns is capped.
		m.HTTPRequest("api", "GET", "GET /v1/agents/"+id, 200, time.Millisecond)
		m.HTTPRequest("api", "BREW", "", 404, time.Millisecond) // unmatched, odd method
		m.ModelRequest("openai-compatible", fmt.Sprintf("model-%d", i), 200, time.Millisecond)
		m.ModelRequest("openai-compatible", "name with spaces and a user@example.com "+id, 200, time.Millisecond)
		m.ModelRequest("openai-compatible", metrics.ModelTeam, 200, time.Millisecond)
		m.ModelTokens("openai-compatible", fmt.Sprintf("model-%d", i), 1, 1, 1, 1)
		m.MCPRequest(fmt.Sprintf("server_%d", i), 200, time.Millisecond)
		m.MCPToolCalls(fmt.Sprintf("server_%d", i), "ok", 1)
		m.RateLimited("apikey:" + id)
		m.RateLimited(fmt.Sprintf("kind%d:%s", i, id))
		m.Wake("agent-sandbox (warm pool pool-"+id+")", "warm", "ok", time.Millisecond)
		m.OAuthTokenError(fmt.Sprintf("made_up_%d", i))
		m.SignIn("made-up", "success", "")
	}
	s := metricstest.Gather(t, m)
	for _, c := range []struct {
		prefix string
		max    int
	}{
		{"pdp_http_requests_total", 1100},
		{"pdp_model_proxy_requests_total", 70},
		{"pdp_model_proxy_tokens_total", 70 * 4},
		{"pdp_mcp_proxy_requests_total", 70},
		{"pdp_mcp_tool_calls_total", 70},
		{"pdp_rate_limit_rejections_total", 20},
		{"pdp_agent_wakes_total", 2},
		{"pdp_oauth_token_errors_total", 2},
		{"pdp_signins_total", 2},
	} {
		if n := s.Series(c.prefix); n == 0 || n > c.max {
			t.Errorf("%s has %d series, want 1 to %d", c.prefix, n, c.max)
		}
	}
	if s.Has("pdp_model_proxy_requests_total", "model", "name with spaces and a user@example.com ") {
		t.Error("a model name with spaces became a label")
	}
	if got := s.Sum("pdp_rate_limit_rejections_total", "limiter", "apikey"); got != 5000 {
		t.Errorf("apikey rejections = %v: the ID after the colon must not matter", got)
	}
	if !s.Has("pdp_agent_wakes_total", "launcher", "agent-sandbox") {
		t.Error("the warm pool's name leaked into the launcher label")
	}
}

// exerciseAll records every kind of observation once (with a pool, so the
// pool metrics exist too).
func exerciseAll(t *testing.T, m *metrics.Metrics) {
	t.Helper()
	m.HTTPRequest("api", "GET", "GET /v1/me", 200, time.Millisecond)
	m.InFlight("api", 1)
	m.Wake("local", "cold", "ok", time.Millisecond)
	m.Wake("agent-sandbox", "warm", "ok", time.Millisecond)
	m.Wake("agent-sandbox", "pool_miss", "ok", time.Millisecond)
	m.Hibernation("idle", nil)
	m.SetAgents(1, 2)
	m.SetAgentsRunning(1)
	m.ModelRequest("openai", "gpt-4o", 200, time.Millisecond)
	m.ModelTokens("openai", "gpt-4o", 1, 1, 1, 1)
	m.MCPRequest("echo", 200, time.Millisecond)
	m.MCPToolCalls("echo", "ok", 1)
	m.OAuthTokenIssued("authorization_code")
	m.OAuthTokenError("invalid_grant")
	m.OAuthRefreshReuse()
	m.SignIn("oidc", "failure", "denied")
	m.RateLimited("login:x")
	m.ThreadSubscribers(1)
	m.Loop("reaper")(errors.New("x"))
	m.Loop("reaper")(nil)
	pool, err := pgxpool.New(context.Background(), "postgres://nobody@127.0.0.1:1/none") // connects lazily
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m.RegisterPool("platform", pool)
}

// labelNames are the label names of every metric the platform exports, by
// metric name (histogram series included).
func labelNames(t *testing.T, m *metrics.Metrics) map[string]map[string]bool {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	for _, f := range families {
		set := map[string]bool{}
		for _, metric := range f.GetMetric() {
			for _, l := range metric.GetLabel() {
				set[l.GetName()] = true
			}
		}
		out[f.GetName()] = set
		if f.GetType() == dto.MetricType_HISTOGRAM {
			set["le"] = true
			for _, suffix := range []string{"_bucket", "_sum", "_count"} {
				out[f.GetName()+suffix] = set
			}
		}
	}
	return out
}

// The label names the platform exports are an allowlist: nothing that
// identifies a person, a team, an agent, a thread or a URL.
func TestLabelNamesAreAllowlisted(t *testing.T) {
	m := newMetrics()
	exerciseAll(t, m)
	allowed := map[string]bool{
		"listener": true, "method": true, "route": true, "code": true, "launcher": true, "start": true, "result": true,
		"enabled": true, "reason": true, "provider_kind": true, "model": true, "direction": true, "server": true,
		"grant_type": true, "error": true, "limiter": true, "loop": true, "pool": true,
		"version": true, "commit": true, "go_version": true, "le": true, "quantile": true,
	}
	checked := 0
	for name, set := range labelNames(t, m) {
		if !strings.HasPrefix(name, "pdp_") {
			continue
		}
		for l := range set {
			checked++
			if !allowed[l] {
				t.Errorf("%s has the label %q, which is not on the allowlist (identifying data is never a label)", name, l)
			}
		}
	}
	if checked < 30 {
		t.Fatalf("only %d labels checked: the test is not exercising the metrics", checked)
	}
}

func TestRecordedValues(t *testing.T) {
	m := newMetrics()
	m.Wake("agent-sandbox (warm pool p)", "warm", "ok", 300*time.Millisecond)
	m.Wake("agent-sandbox (warm pool p)", "pool_miss", "ok", 9*time.Second)
	m.Wake("agent-sandbox (warm pool p)", "running", "ok", time.Millisecond)
	m.Wake("kubernetes", "unknown", "timeout", time.Minute)
	m.Hibernation("idle", nil)
	m.Hibernation("request", errors.New("boom"))
	m.ModelRequest("anthropic", "claude", 429, time.Second)
	m.ModelTokens("anthropic", "claude", 100, 20, 0, 5)
	m.Loop("reaper")(nil)
	m.Loop("cleanup")(errors.New("x"))
	m.RateLimited("login:abc")
	m.SignIn("dev", "success", "")
	s := metricstest.Gather(t, m)
	checks := []struct {
		name string
		kv   []string
		want float64
	}{
		{"pdp_agent_wakes_total", []string{"launcher", "agent-sandbox", "start", "warm"}, 1},
		{"pdp_warm_pool_claims_total", []string{"result", "hit"}, 1},
		{"pdp_warm_pool_claims_total", []string{"result", "miss"}, 1},
		{"pdp_agent_wake_duration_seconds_count", []string{"start", "pool_miss"}, 1},
		{"pdp_agent_wakes_total", []string{"result", "timeout"}, 1},
		{"pdp_agent_hibernations_total", []string{"reason", "idle", "result", "ok"}, 1},
		{"pdp_agent_hibernations_total", []string{"reason", "request", "result", "error"}, 1},
		{"pdp_model_proxy_requests_total", []string{"provider_kind", "anthropic", "model", "claude", "code", "4xx"}, 1},
		{"pdp_model_proxy_tokens_total", []string{"direction", "input"}, 100},
		{"pdp_model_proxy_tokens_total", []string{"direction", "output"}, 20},
		{"pdp_model_proxy_tokens_total", []string{"direction", "cache_write"}, 5},
		{"pdp_background_runs_total", []string{"loop", "reaper", "result", "ok"}, 1},
		{"pdp_background_runs_total", []string{"loop", "cleanup", "result", "error"}, 1},
		{"pdp_rate_limit_rejections_total", []string{"limiter", "login"}, 1},
		{"pdp_signins_total", []string{"method", "dev", "result", "success", "reason", "none"}, 1},
	}
	for _, c := range checks {
		if got := s.Sum(c.name, c.kv...); got != c.want {
			t.Errorf("%s%v = %v, want %v", c.name, c.kv, got, c.want)
		}
	}
	// A failed loop run does not move the last-success time; a good one does.
	if !s.Has("pdp_background_last_success_timestamp_seconds", "loop", "reaper") || s.Has("pdp_background_last_success_timestamp_seconds", "loop", "cleanup") {
		t.Error("last-success timestamps are wrong")
	}
}
