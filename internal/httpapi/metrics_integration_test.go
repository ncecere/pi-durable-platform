package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"gopkg.in/yaml.v3"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/metrics/metricstest"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// traceparentLog is a reverse proxy in front of an upstream that records the
// traceparent header of every request it relays.
type traceparentLog struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []string
}

func newTraceparentLog(t *testing.T, upstream *httptest.Server) *traceparentLog {
	l := &traceparentLog{}
	target, _ := url.Parse(upstream.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	l.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.mu.Lock()
		l.got = append(l.got, r.Header.Get("Traceparent"))
		l.mu.Unlock()
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(l.srv.Close)
	return l
}

func (l *traceparentLog) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.got...)
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// The whole instrumentation against a running app: an agent wakes, a
// conversation goes through the model proxy and the MCP proxy, requests are
// refused, and the metrics a scraper reads and the spans an exporter gets
// say so, with nothing that identifies anyone or anything.
func TestMetricsAndTraces(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	catalog := newTraceparentLog(t, newFakeMCP(t, "catalog", "Bearer catalog-secret").srv)
	model := newTraceparentLog(t, newToolCallingModel(t))
	var m *metrics.Metrics
	app := newTestAppDeps(t, nil, testutil.Logger(), func(d *httpapi.Deps) { m = d.Metrics })
	e := newTeamEnvOn(t, app)
	// The metrics listener, as the platform serves it (its own port).
	scrape := httptest.NewServer(m.NewServer("").Handler)
	t.Cleanup(scrape.Close)

	var srv apitypes.McpServer
	code, ec := e.admin.call("POST", "/v1/admin/mcp-servers", map[string]any{"name": "Fake Tools", "url": catalog.srv.URL + "/mcp",
		"headers": map[string]string{"Authorization": "Bearer catalog-secret"}, "visibility": "teams", "teams": []string{"tools"}}, &srv, nil)
	mustCode(t, "add the MCP server", code, ec, 201, "")
	var prov apitypes.ModelProvider
	code, ec = e.admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "Tool LLM", "kind": "openai-compatible",
		"baseUrl": model.srv.URL + "/v1", "apiKey": "llm-secret-key"}, &prov, nil)
	mustCode(t, "add the model provider", code, ec, 201, "")
	var mod apitypes.Model
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "tool-model"}, &mod, nil)
	mustCode(t, "add the model", code, ec, 201, "")
	var tpl apitypes.AgentTemplate
	code, ec = e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "mcp", "name": "MCP", "instructions": "Use tools.",
		"modelId": mod.Id, "mcp": []any{map[string]any{"serverId": srv.Id, "tools": []string{"echo"}}}}, &tpl, nil)
	mustCode(t, "a template", code, ec, 201, "")
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Metrics bot", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	messages := base + "/threads/private-thread-t1/messages"

	// A conversation: wakes the agent, calls the model twice and the tool once.
	const prompt = "use mcp__fake_tools__echo"
	code, ec = e.member.call("POST", messages, map[string]any{"text": prompt}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	v := waitReply(t, e.member, base+"/threads/private-thread-t1", 4)
	if got := text(v.Messages[len(v.Messages)-1]); !strings.Contains(got, "catalog echo: hi") {
		t.Fatalf("reply %q", got)
	}

	// Refusals: the API (an agent of another team), the gateway (a bad
	// credential), the MCP proxy (a tool the template left out), a sign-in.
	code, ec = e.outsider.call("GET", base, nil, nil, nil)
	mustCode(t, "another team's agent", code, ec, 404, "")
	req, _ := http.NewRequest("POST", app.GatewayURL+"/model-proxy/"+prov.Id.String()+"/chat/completions", bytes.NewReader([]byte(`{"model":"tool-model"}`)))
	req.Header.Set("Authorization", "Bearer pdpm."+ag.Id.String()+".wrong")
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != 401 {
		t.Fatalf("a bad gateway credential: %v %v", res, err)
	}
	cred := e.scalarSpecKey(e.spec(ag.Id))
	req, _ = http.NewRequest("POST", app.GatewayURL+"/mcp-proxy/server/"+srv.Id.String(),
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"secret_op","arguments":{}}}`)))
	req.Header.Set("Authorization", "Bearer "+cred)
	req.Header.Set("Content-Type", "application/json")
	if res, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	if code, _ := app.do("POST", "/auth/dev", map[string]string{"account": "nobody"}, nil); code != 400 {
		t.Fatalf("an unknown account: %d", code)
	}
	// Many different IDs and paths must not make many series.
	for i := 0; i < 150; i++ {
		e.member.call("GET", "/v1/agents/"+uuid.NewString(), nil, nil, nil)
		e.member.call("GET", "/v1/teams/team-"+fmt.Sprint(i), nil, nil, nil)
		app.do("GET", fmt.Sprintf("/no/such/path/%d/%s", i, uuid.NewString()), nil, nil)
	}

	// The reaper's view of what runs, the agents that exist, and an explicit hibernate.
	app.Agents.Host().ReapOnce(context.Background())
	ctx, stop := context.WithCancel(context.Background())
	go app.Agents.ObserveAgents(ctx, time.Hour)
	t.Cleanup(stop)
	// The observer's first pass sees the agent running; hibernate only after it has reported.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if metricstest.Fetch(t, scrape.URL+"/metrics").Sum("pdp_agents", "enabled", "true") >= 1 || time.Now().After(deadline) {
			break
		}
	}
	code, ec = e.member.call("POST", base+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate", code, ec, 200, "")
	s := metricstest.Fetch(t, scrape.URL+"/metrics")

	t.Run("metrics", func(t *testing.T) {
		geq := func(name string, want float64, kv ...string) {
			t.Helper()
			if got := s.Sum(name, kv...); got < want {
				t.Errorf("%s%v = %v, want at least %v", name, kv, got, want)
			}
		}
		eq := func(name string, want float64, kv ...string) {
			t.Helper()
			if got := s.Sum(name, kv...); got != want {
				t.Errorf("%s%v = %v, want %v", name, kv, got, want)
			}
		}
		// HTTP, by route pattern: the message was accepted, other teams' agents are 404s.
		eq("pdp_http_requests_total", 1, "listener", "api", "method", "POST", "route", "/v1/agents/{agentId}/threads/{threadId}/messages", "code", "2xx")
		geq("pdp_http_requests_total", 151, "listener", "api", "method", "GET", "route", "/v1/agents/{agentId}", "code", "4xx")
		geq("pdp_http_requests_total", 150, "listener", "api", "route", "unmatched") // the web app's fallback, whatever the path
		geq("pdp_http_requests_total", 150, "listener", "api", "method", "GET", "route", "/v1/teams/{team}", "code", "4xx")
		geq("pdp_http_request_duration_seconds_count", 1, "listener", "api", "route", "/v1/me")
		geq("pdp_http_requests_total", 2, "listener", "gateway", "method", "POST", "route", "/model-proxy/{providerId}/{path...}", "code", "2xx")
		eq("pdp_http_requests_total", 1, "listener", "gateway", "route", "/model-proxy/{providerId}/{path...}", "code", "4xx")
		// The agent woke once from nothing, and the other calls found it running.
		eq("pdp_agent_wakes_total", 1, "launcher", "local", "start", "cold", "result", "ok")
		geq("pdp_agent_wakes_total", 1, "launcher", "local", "start", "running", "result", "ok")
		eq("pdp_agent_wake_duration_seconds_count", 1, "launcher", "local", "start", "cold")
		eq("pdp_agents_running", 1)
		geq("pdp_agents", 1, "enabled", "true")
		eq("pdp_agent_hibernations_total", 1, "reason", "request", "result", "ok")
		// The model proxy: two calls of 10 tokens in and 5 out each, and the model named.
		calls := s.Sum("pdp_model_proxy_requests_total", "provider_kind", "openai-compatible", "model", "tool-model", "code", "2xx")
		if calls < 2 {
			t.Errorf("model proxy calls = %v", calls)
		}
		eq("pdp_model_proxy_tokens_total", 10*calls, "model", "tool-model", "direction", "input")
		eq("pdp_model_proxy_tokens_total", 5*calls, "model", "tool-model", "direction", "output")
		eq("pdp_model_proxy_requests_total", 1, "provider_kind", "unknown", "model", "unknown", "code", "4xx")
		geq("pdp_model_proxy_request_duration_seconds_count", 2, "model", "tool-model")
		// The MCP proxy: the platform server by slug, one call that went through, one refused.
		geq("pdp_mcp_proxy_requests_total", 1, "server", "fake_tools", "code", "2xx")
		eq("pdp_mcp_tool_calls_total", 1, "server", "fake_tools", "result", "ok")
		eq("pdp_mcp_tool_calls_total", 1, "server", "fake_tools", "result", "denied")
		// Sign-in, the databases, the build.
		geq("pdp_signins_total", 6, "method", "dev", "result", "success")
		eq("pdp_signins_total", 1, "method", "dev", "result", "failure", "reason", "invalid")
		geq("pdp_db_pool_max_connections", 1, "pool", "platform")
		geq("pdp_db_pool_max_connections", 1, "pool", "agents")
		geq("pdp_db_pool_acquires_total", 1, "pool", "platform")
		if !s.Has("pdp_build_info") || !s.Has("go_goroutines") || !s.Has("pdp_thread_event_subscribers") {
			t.Error("build info, Go runtime or thread subscribers missing")
		}
		eq("pdp_background_runs_total", 1, "loop", "agent_count", "result", "ok")

		// No label value holds an ID, an address, a thread or a path.
		forbidden := []string{"private-thread-t1", "localhost", "127.0.0.1", "@", ag.Id.String(), "no/such", "pdpm."}
		for name, samples := range s.Samples {
			for _, x := range samples {
				for k, val := range x.Labels {
					if uuidRE.MatchString(val) {
						t.Errorf("%s{%s=%q} holds an ID", name, k, val)
					}
					for _, f := range forbidden {
						if strings.Contains(val, f) {
							t.Errorf("%s{%s=%q} holds %q", name, k, val, f)
						}
					}
				}
			}
		}
		// 450 requests to many different paths and IDs, a handful of series.
		if n := s.Series("pdp_http_requests_total"); n > 40 {
			t.Errorf("pdp_http_requests_total has %d series", n)
		}
		for _, secret := range []string{"catalog-secret", "llm-secret-key", cred, prompt} {
			if strings.Contains(s.Text, secret) {
				t.Errorf("the scrape holds %q", secret)
			}
		}
	})

	// Every metric the dashboard and the example alerts query is in what the
	// running app serves, except those of flows this test doesn't drive (OAuth
	// has its own test, rate limiting the one below).
	t.Run("dashboard", func(t *testing.T) {
		otherFlows := map[string]bool{"pdp_oauth_tokens_issued_total": true, "pdp_oauth_token_errors_total": true,
			"pdp_oauth_refresh_reuse_revocations_total": true, "pdp_rate_limit_rejections_total": true}
		used := map[string]bool{}
		for _, expr := range panelAndAlertExprs(t) {
			for _, name := range regexp.MustCompile(`\b(?:pdp|go|process)_[a-z0-9_]+`).FindAllString(expr, -1) {
				used[name] = true
			}
		}
		if len(used) < 30 {
			t.Fatalf("only %d metrics found in the dashboard and alerts", len(used))
		}
		for name := range used {
			base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
			if otherFlows[name] {
				continue
			}
			if !s.Has(name) && !s.Has(base) {
				t.Errorf("%s is queried by the dashboard or the alerts but the running app does not export it", name)
			}
		}
	})

	t.Run("ports", func(t *testing.T) {
		// Neither the web listener nor the gateway serves metrics, and the metrics listener serves only /metrics.
		for _, base := range []string{app.URL, app.GatewayURL} {
			res, err := http.Get(base + "/metrics")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if strings.Contains(string(body), "# HELP") || strings.Contains(string(body), "pdp_") || strings.Contains(res.Header.Get("Content-Type"), "text/plain; version") {
				t.Errorf("%s/metrics serves metrics: %d %.80s", base, res.StatusCode, body)
			}
		}
		for _, path := range []string{"/", "/v1/me", "/healthz", "/readyz"} {
			res, err := http.Get(scrape.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 404 {
				t.Errorf("the metrics listener serves %s: %d", path, res.StatusCode)
			}
		}
	})

	t.Run("traces", func(t *testing.T) {
		spans := exp.GetSpans()
		byName := map[string][]tracetest.SpanStub{}
		for _, sp := range spans {
			byName[sp.Name] = append(byName[sp.Name], sp)
		}
		attr := func(sp tracetest.SpanStub, key string) string {
			for _, a := range sp.Attributes {
				if string(a.Key) == key {
					return a.Value.Emit()
				}
			}
			return ""
		}
		child := func(parent tracetest.SpanStub, name string) (tracetest.SpanStub, bool) {
			for _, sp := range byName[name] {
				if sp.Parent.SpanID() == parent.SpanContext.SpanID() && sp.SpanContext.TraceID() == parent.SpanContext.TraceID() {
					return sp, true
				}
			}
			return tracetest.SpanStub{}, false
		}

		// API -> the runtime: the server span is named by route pattern, with the wake and the runtime request below it.
		api := byName["POST /v1/agents/{agentId}/threads/{threadId}/messages"]
		if len(api) != 1 || attr(api[0], "http.route") != "/v1/agents/{agentId}/threads/{threadId}/messages" || attr(api[0], "http.response.status_code") != "202" {
			t.Fatalf("API server spans: %+v", api)
		}
		wake, ok := child(api[0], "agent.wake")
		if !ok || attr(wake, "pdp.launcher") != "local" || attr(wake, "pdp.wake.start") != "cold" || attr(wake, "pdp.wake.result") != "ok" {
			t.Errorf("the wake span: %v %+v", ok, wake.Attributes)
		}
		if rt, ok := child(api[0], "agent.runtime_request"); !ok || attr(rt, "http.response.status_code") != "202" {
			t.Errorf("the runtime request span: %v %+v", ok, rt.Attributes)
		}

		// The gateway: a model proxy request with the upstream call below it, whose trace context the provider got.
		var upstream []tracetest.SpanStub
		for _, gw := range byName["POST /model-proxy/{providerId}/{path...}"] {
			if sp, ok := child(gw, "model.upstream"); ok {
				upstream = append(upstream, sp)
				if attr(sp, "pdp.provider.kind") != "openai-compatible" || attr(sp, "pdp.model") != "tool-model" || attr(sp, "http.response.status_code") != "200" {
					t.Errorf("the model upstream span: %+v", sp.Attributes)
				}
			}
		}
		if len(upstream) < 2 {
			t.Fatalf("model upstream spans: %d", len(upstream))
		}
		wantTP := map[string]bool{}
		for _, sp := range upstream {
			wantTP[fmt.Sprintf("00-%s-%s-01", sp.SpanContext.TraceID(), sp.SpanContext.SpanID())] = true
		}
		for _, tp := range model.seen() {
			if !wantTP[tp] {
				t.Errorf("the model provider got traceparent %q, not one of its upstream spans'", tp)
			}
		}
		if len(model.seen()) < 2 {
			t.Errorf("the provider was called %d times", len(model.seen()))
		}

		// The MCP proxy: the tool call's upstream span names the tool, and the server got the trace context.
		var mcpUp []tracetest.SpanStub
		for _, sp := range byName["mcp.upstream"] {
			if strings.Contains(attr(sp, "pdp.mcp.tools"), "echo") {
				mcpUp = append(mcpUp, sp)
			}
		}
		if len(mcpUp) != 1 || attr(mcpUp[0], "pdp.mcp.server") != "fake_tools" || attr(mcpUp[0], "pdp.mcp.tool_calls") != "1" {
			t.Fatalf("the MCP upstream span for the tool call: %+v", mcpUp)
		}
		want := fmt.Sprintf("00-%s-%s-01", mcpUp[0].SpanContext.TraceID(), mcpUp[0].SpanContext.SpanID())
		found := false
		for _, tp := range catalog.seen() {
			found = found || tp == want
		}
		if !found {
			t.Errorf("the MCP server never got traceparent %s (saw %v)", want, catalog.seen())
		}

		// Nothing secret or personal is in a span: not the keys, the prompt,
		// the credential, a thread, an address, the Authorization header, a URL.
		banned := []string{"catalog-secret", "llm-secret-key", "pdpm.", cred, prompt, "private-thread-t1", "127.0.0.1", "@localhost", "Bearer", "session"}
		bannedKeys := regexp.MustCompile(`(?i)url|header|path|target|user|email|cookie|authorization|body|message|content|prompt`)
		for _, sp := range spans {
			check := func(what, val string) {
				for _, b := range banned {
					if strings.Contains(val, b) {
						t.Errorf("span %q: %s holds %q", sp.Name, what, b)
					}
				}
			}
			check("its name", sp.Name)
			check("its status", sp.Status.Description)
			for _, a := range sp.Attributes {
				check("attribute "+string(a.Key), a.Value.Emit())
				if k := string(a.Key); bannedKeys.MatchString(k) && k != "http.route" && k != "pdp.mcp.tools" && k != "pdp.mcp.tool_calls" {
					t.Errorf("span %q has the attribute %q", sp.Name, k)
				}
			}
			for _, ev := range sp.Events {
				check("an event", ev.Name)
			}
		}
	})
}

// A refused request is counted by the rate limiter: the per-person limit.
func TestMetricsCountRateLimitRejections(t *testing.T) {
	var m *metrics.Metrics
	app := newTestAppDeps(t, func(c *config.Config) { c.RequestsPerMinute = 5 }, testutil.Logger(), func(d *httpapi.Deps) { m = d.Metrics })
	app.devLogin("user")
	limited := 0
	for i := 0; i < 20; i++ {
		if code, _ := app.do("GET", "/v1/me", nil, nil); code == 429 {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("the limit never applied")
	}
	s := metricstest.Gather(t, m)
	if got := s.Sum("pdp_rate_limit_rejections_total", "limiter", "user"); got != float64(limited) {
		t.Errorf("rejections = %v, want %d", got, limited)
	}
	if got := s.Sum("pdp_http_requests_total", "route", "/v1/me", "code", "4xx"); got != float64(limited) {
		t.Errorf("429s in the HTTP metrics = %v, want %d", got, limited)
	}
	if n := len(s.LabelValues("pdp_rate_limit_rejections_total", "limiter")); n != 1 {
		t.Errorf("limiters: %v", s.LabelValues("pdp_rate_limit_rejections_total", "limiter"))
	}
}

// panelAndAlertExprs are the PromQL expressions of the dashboard's panels and
// the example alerts.
func panelAndAlertExprs(t *testing.T) []string {
	t.Helper()
	var exprs []string
	raw, err := os.ReadFile("../../deploy/grafana/pi-durable-platform.json")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Panels []struct {
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Panels {
		for _, tg := range p.Targets {
			exprs = append(exprs, tg.Expr)
		}
	}
	raw, err = os.ReadFile("../../deploy/grafana/alerts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var rule struct {
		Spec struct {
			Groups []struct {
				Rules []struct {
					Expr string `yaml:"expr"`
				} `yaml:"rules"`
			} `yaml:"groups"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	for _, g := range rule.Spec.Groups {
		for _, r := range g.Rules {
			exprs = append(exprs, r.Expr)
		}
	}
	return exprs
}
