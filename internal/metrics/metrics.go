// Package metrics is the platform's Prometheus instrumentation.
//
// One *Metrics owns one registry and every collector; the rest of the code
// calls its small, typed methods at the choke points (the HTTP middleware,
// the agent manager, the model and MCP proxies, OAuth, sign-in, the rate
// limiter and the background loops). Every method accepts a nil receiver and
// then does nothing, so a package works without metrics (tests, commands
// that don't serve).
//
// Label policy (DESIGN.md, Observability): labels hold only small, bounded
// sets of values the platform itself defines: route patterns (never paths),
// status classes, launcher modes, provider kinds, loop names. Nothing that
// identifies a person, a team, an agent, a thread or a URL is ever a label.
// Model names and platform MCP server slugs are labels because the platform
// administrators choose them; they go through a bounded label guard, and a
// team's own models and servers are reported as one value ("team").
package metrics

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Listener names (the `listener` label of the HTTP metrics).
const (
	ListenerAPI     = "api"
	ListenerGateway = "gateway"
)

// Values of the `server` label of the MCP proxy metrics for servers that are
// not the platform's.
const (
	ServerTeam = "team" // a team's own MCP server
	ServerApp  = "app"  // an MCP server an app gave one thread
)

// ModelTeam is the `model` label of a team's own model.
const ModelTeam = "team"

const unknown = "unknown"

// Info identifies the build (the pdp_build_info metric).
type Info struct {
	Version, Commit string
}

// Metrics holds the registry and the collectors.
type Metrics struct {
	reg *prometheus.Registry

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInFlight *prometheus.GaugeVec

	wakes        *prometheus.CounterVec
	wakeDuration *prometheus.HistogramVec
	warmClaims   *prometheus.CounterVec
	agentsRun    prometheus.Gauge
	agents       *prometheus.GaugeVec
	hibernations *prometheus.CounterVec
	awake        *prometheus.GaugeVec
	bgAborts     *prometheus.CounterVec

	modelRequests *prometheus.CounterVec
	modelDuration *prometheus.HistogramVec
	modelTokens   *prometheus.CounterVec

	mcpRequests *prometheus.CounterVec
	mcpDuration *prometheus.HistogramVec
	mcpCalls    *prometheus.CounterVec

	oauthIssued  *prometheus.CounterVec
	oauthErrors  *prometheus.CounterVec
	oauthReuse   prometheus.Counter
	signIns      *prometheus.CounterVec
	rateLimited  *prometheus.CounterVec
	subscribers  prometheus.Gauge
	loopRuns     *prometheus.CounterVec
	loopDuration *prometheus.HistogramVec
	loopLast     *prometheus.GaugeVec

	pools   *poolCollector
	recheck *groupRecheck // grouprecheck.go

	models, servers, limiters, kinds, routes *labelGuard
}

// New returns metrics on a registry of their own, with the Go runtime and
// process collectors and the build info.
func New(info Info) *Metrics {
	m := &Metrics{
		reg:      prometheus.NewRegistry(),
		models:   newLabelGuard(64, 64),
		servers:  newLabelGuard(64, 64),
		limiters: newLabelGuard(16, 32),
		kinds:    newLabelGuard(16, 32),
		routes:   newLabelGuard(1000, 160),
		pools:    &poolCollector{},
	}
	wakeBuckets := []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60, 120}
	proxyBuckets := []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 20, 30, 60, 120, 300}
	httpBuckets := []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300}

	m.httpRequests = counterVec("http_requests_total", "HTTP requests served, by listener (api or gateway), method, route pattern and status class.", "listener", "method", "route", "code")
	m.httpDuration = histogramVec("http_request_duration_seconds", "HTTP request duration by listener, method and route pattern, until the response is complete (event streams last as long as they are open).", httpBuckets, "listener", "method", "route")
	m.httpInFlight = gaugeVec("http_requests_in_flight", "HTTP requests being served now.", "listener")

	m.wakes = counterVec("agent_wakes_total", "Calls that made sure an agent's runtime was up, by launcher mode, how it got a runtime (start: running, cold, warm, pool_miss, unknown) and result (ok, error, canceled, timeout).", "launcher", "start", "result")
	m.wakeDuration = histogramVec("agent_wake_duration_seconds", "How long making sure an agent's runtime was up took (start=running is the fast path: nothing had to start).", wakeBuckets, "launcher", "start", "result")
	m.warmClaims = counterVec("warm_pool_claims_total", "Claims on the Agent Sandbox warm pool: hit (a standby runtime was adopted) or miss (the pool was empty and a runtime was created on demand).", "result")
	m.agentsRun = gauge("agents_running", "Agents with a live runtime, as last seen by the idle reaper.")
	m.agents = gaugeVec("agents", "Agents that exist, by whether they are enabled (the same on every replica: use max, not sum).", "enabled")
	m.hibernations = counterVec("agent_hibernations_total", "Runtimes stopped: idle (the reaper) or request (an explicit hibernate, or a change that restarts the agent).", "reason", "result")
	m.awake = gaugeVec("agents_awake", "Running agents the idle reaper last found kept awake, by reason: reply (a thread replying), stream (an open event stream), background (background work under AGENTS_BACKGROUND_MAX), recent (activity within the idle timeout). An agent can count for several.", "reason")
	m.bgAborts = counterVec("agent_background_aborts_total", "Background work past AGENTS_BACKGROUND_MAX that the reaper had a runtime abort before hibernating it, by result (ok, error).", "result")

	m.modelRequests = counterVec("model_proxy_requests_total", "Model proxy requests by provider kind, model (the platform's models by name, a team's own as \"team\") and status class.", "provider_kind", "model", "code")
	m.modelDuration = histogramVec("model_proxy_request_duration_seconds", "Model proxy request duration (the whole reply, streamed replies included) by provider kind and model.", proxyBuckets, "provider_kind", "model")
	m.modelTokens = counterVec("model_proxy_tokens_total", "Tokens the model proxy saw replies report, by provider kind, model and direction (input, output, cache_read, cache_write).", "provider_kind", "model", "direction")

	m.mcpRequests = counterVec("mcp_proxy_requests_total", "MCP proxy requests by server (the platform's by slug; \"team\" for a team's own, \"app\" for an app's) and status class.", "server", "code")
	m.mcpDuration = histogramVec("mcp_proxy_request_duration_seconds", "MCP proxy request duration by server.", proxyBuckets, "server")
	m.mcpCalls = counterVec("mcp_tool_calls_total", "MCP tool calls through the proxy by server and result (ok, error, denied). Tool names are not labels.", "server", "result")

	m.oauthIssued = counterVec("oauth_tokens_issued_total", "OAuth Connect token pairs issued, by grant type (authorization_code, refresh_token).", "grant_type")
	m.oauthErrors = counterVec("oauth_token_errors_total", "OAuth Connect token requests refused, by OAuth error code.", "error")
	m.oauthReuse = counter("oauth_refresh_reuse_revocations_total", "Grants revoked because a refresh token that was already rotated was presented again.")
	m.signIns = counterVec("signins_total", "Sign-ins by method (oidc, dev), result (success, failure) and, for failures, the reason (denied, invalid, unavailable, error; none for successes).", "method", "result", "reason")
	m.rateLimited = counterVec("rate_limit_rejections_total", "Requests refused by a rate limit, by limiter (login, request, oauth, ...).", "limiter")
	m.subscribers = gauge("thread_event_subscribers", "Open thread event streams on this replica.")
	m.loopRuns = counterVec("background_runs_total", "Background loop runs by loop (reaper, cleanup, rate_cleanup, group_recheck, thread_events, agent_count, triggers) and result (ok, error).", "loop", "result")
	m.loopDuration = histogramVec("background_run_duration_seconds", "Background loop run duration by loop.", []float64{.01, .05, .1, .5, 1, 5, 15, 60, 300}, "loop")
	m.loopLast = gaugeVec("background_last_success_timestamp_seconds", "Unix time of the last successful run of each background loop.", "loop")

	// Series that rate() and alerts compare by ratio exist from the start, at
	// zero (a counter that appears with its first event loses that event).
	for _, r := range []string{"hit", "miss"} {
		m.warmClaims.WithLabelValues(r)
	}
	for _, reason := range []string{"idle", "request"} {
		for _, result := range []string{"ok", "error"} {
			m.hibernations.WithLabelValues(reason, result)
		}
	}
	m.agents.WithLabelValues("true")
	m.agents.WithLabelValues("false")
	for _, reason := range AwakeReasons {
		m.awake.WithLabelValues(reason)
	}
	for _, result := range []string{"ok", "error"} {
		m.bgAborts.WithLabelValues(result)
	}

	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "pdp", Name: "build_info", Help: "The build: version and commit (always 1).",
		ConstLabels: prometheus.Labels{"version": info.Version, "commit": info.Commit, "go_version": runtime.Version()},
	})
	buildInfo.Set(1)

	m.reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo, m.pools,
		m.httpRequests, m.httpDuration, m.httpInFlight,
		m.wakes, m.wakeDuration, m.warmClaims, m.agentsRun, m.agents, m.hibernations, m.awake, m.bgAborts,
		m.modelRequests, m.modelDuration, m.modelTokens,
		m.mcpRequests, m.mcpDuration, m.mcpCalls,
		m.oauthIssued, m.oauthErrors, m.oauthReuse, m.signIns, m.rateLimited, m.subscribers,
		m.loopRuns, m.loopDuration, m.loopLast,
	)
	m.recheck = newGroupRecheck(m.reg)
	return m
}

// Registry is the registry (tests; to add a collector).
func (m *Metrics) Registry() *prometheus.Registry {
	if m == nil {
		return nil
	}
	return m.reg
}

// Handler serves the registry in the Prometheus text format, and nothing
// else: any other path is a 404.
func (m *Metrics) Handler() http.Handler {
	mux := http.NewServeMux()
	if m != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
			// A failing collector must not hide the others.
			ErrorHandling: promhttp.ContinueOnError,
		}))
	}
	return mux
}

// NewServer is the metrics listener: Handler on addr, with timeouts (the
// scrape is a small, quick request).
func (m *Metrics) NewServer(addr string) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           m.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

// --- HTTP ---

// InFlight adds delta to the requests being served on listener.
func (m *Metrics) InFlight(listener string, delta float64) {
	if m == nil {
		return
	}
	m.httpInFlight.WithLabelValues(listener).Add(delta)
}

// HTTPRequest records a served request: pattern is the mux's pattern for it
// ("GET /v1/teams/{team}", or "" when nothing matched), never the path.
func (m *Metrics) HTTPRequest(listener, method, pattern string, status int, d time.Duration) {
	if m == nil {
		return
	}
	route := m.routes.clean(RoutePattern(pattern))
	method = Method(method)
	m.httpRequests.WithLabelValues(listener, method, route, StatusClass(status)).Inc()
	m.httpDuration.WithLabelValues(listener, method, route).Observe(d.Seconds())
}

// --- Agents ---

// Wake records a call that made sure an agent's runtime was up.
func (m *Metrics) Wake(launcherKind, start, result string, d time.Duration) {
	if m == nil {
		return
	}
	mode := LauncherMode(launcherKind)
	m.wakes.WithLabelValues(mode, start, result).Inc()
	m.wakeDuration.WithLabelValues(mode, start, result).Observe(d.Seconds())
	switch start {
	case "warm":
		m.warmClaims.WithLabelValues("hit").Inc()
	case "pool_miss":
		m.warmClaims.WithLabelValues("miss").Inc()
	}
}

// SetAgentsRunning sets the agents with a live runtime.
func (m *Metrics) SetAgentsRunning(n int) {
	if m == nil {
		return
	}
	m.agentsRun.Set(float64(n))
}

// SetAgents sets the agents that exist.
func (m *Metrics) SetAgents(enabled, disabled int64) {
	if m == nil {
		return
	}
	m.agents.WithLabelValues("true").Set(float64(enabled))
	m.agents.WithLabelValues("false").Set(float64(disabled))
}

// Hibernation records a runtime being stopped; reason is "idle" or "request".
func (m *Metrics) Hibernation(reason string, err error) {
	if m == nil {
		return
	}
	m.hibernations.WithLabelValues(reason, okOrError(err)).Inc()
}

// AwakeReasons are the values of agents_awake's reason label: why the reaper
// left a running agent awake.
var AwakeReasons = []string{"reply", "stream", "background", "recent"}

// SetAgentsAwake sets, for each of AwakeReasons, how many running agents the
// reaper found kept awake by it (missing reasons: none).
func (m *Metrics) SetAgentsAwake(byReason map[string]int) {
	if m == nil {
		return
	}
	for _, reason := range AwakeReasons {
		m.awake.WithLabelValues(reason).Set(float64(byReason[reason]))
	}
}

// BackgroundAbort counts a reaper's request to abort background work past
// the ceiling.
func (m *Metrics) BackgroundAbort(err error) {
	if m == nil {
		return
	}
	m.bgAborts.WithLabelValues(okOrError(err)).Inc()
}

// --- Proxies ---

// ModelRequest records a model proxy request. providerKind is the provider's
// kind ("" when the request was refused before the model was known), model
// the platform model's upstream name, or ModelTeam for a team's own.
func (m *Metrics) ModelRequest(providerKind, model string, status int, d time.Duration) {
	if m == nil {
		return
	}
	kind, model := m.providerKind(providerKind), m.modelLabel(model)
	m.modelRequests.WithLabelValues(kind, model, StatusClass(status)).Inc()
	m.modelDuration.WithLabelValues(kind, model).Observe(d.Seconds())
}

// ModelTokens adds the tokens a model proxy reply reported.
func (m *Metrics) ModelTokens(providerKind, model string, input, output, cacheRead, cacheWrite int64) {
	if m == nil {
		return
	}
	kind, model := m.providerKind(providerKind), m.modelLabel(model)
	for dir, n := range map[string]int64{"input": input, "output": output, "cache_read": cacheRead, "cache_write": cacheWrite} {
		if n > 0 {
			m.modelTokens.WithLabelValues(kind, model, dir).Add(float64(n))
		}
	}
}

func (m *Metrics) providerKind(k string) string {
	if k == "" {
		return unknown
	}
	return m.kinds.clean(k) // provider kinds are the platform's own few
}

func (m *Metrics) modelLabel(model string) string {
	if model == "" {
		return unknown
	}
	if model == ModelTeam {
		return ModelTeam
	}
	return m.models.clean(model)
}

// MCPRequest records an MCP proxy request. server is the platform server's
// slug, ServerTeam or ServerApp.
func (m *Metrics) MCPRequest(server string, status int, d time.Duration) {
	if m == nil {
		return
	}
	server = m.serverLabel(server)
	m.mcpRequests.WithLabelValues(server, StatusClass(status)).Inc()
	m.mcpDuration.WithLabelValues(server).Observe(d.Seconds())
}

// MCPToolCalls records n tool calls with a result (ok, error, denied).
func (m *Metrics) MCPToolCalls(server, result string, n int) {
	if m == nil || n <= 0 {
		return
	}
	m.mcpCalls.WithLabelValues(m.serverLabel(server), result).Add(float64(n))
}

func (m *Metrics) serverLabel(s string) string {
	switch s {
	case "":
		return unknown
	case ServerTeam, ServerApp:
		return s
	}
	return m.servers.clean(s)
}

// --- OAuth, sign-in, rate limits ---

// OAuthTokenIssued counts a token pair issued by a grant type.
func (m *Metrics) OAuthTokenIssued(grantType string) {
	if m == nil {
		return
	}
	m.oauthIssued.WithLabelValues(oneOf(grantType, "authorization_code", "refresh_token")).Inc()
}

// OAuthTokenError counts a refused token request by its OAuth error code.
func (m *Metrics) OAuthTokenError(code string) {
	if m == nil {
		return
	}
	m.oauthErrors.WithLabelValues(oneOf(code, "invalid_request", "invalid_client", "invalid_grant", "invalid_target",
		"invalid_scope", "unsupported_grant_type", "server_error")).Inc()
}

// OAuthRefreshReuse counts a grant revoked for refresh token reuse.
func (m *Metrics) OAuthRefreshReuse() {
	if m == nil {
		return
	}
	m.oauthReuse.Inc()
}

// SignIn counts a sign-in. result is "success" or "failure"; reason is one
// of denied, invalid, unavailable, error ("" for a success).
func (m *Metrics) SignIn(method, result, reason string) {
	if m == nil {
		return
	}
	if reason == "" {
		reason = "none"
	}
	m.signIns.WithLabelValues(oneOf(method, "oidc", "dev"), oneOf(result, "success", "failure"),
		oneOf(reason, "none", "denied", "invalid", "unavailable", "error")).Inc()
}

// RateLimited counts a request refused by a rate limit. bucket is the rate
// limit's bucket name: only its kind (the part before the first colon,
// which never holds a person) is used.
func (m *Metrics) RateLimited(bucket string) {
	if m == nil {
		return
	}
	kind, _, _ := strings.Cut(bucket, ":")
	m.rateLimited.WithLabelValues(m.limiters.clean(kind)).Inc()
}

// ThreadSubscribers adds delta to the open thread event streams.
func (m *Metrics) ThreadSubscribers(delta float64) {
	if m == nil {
		return
	}
	m.subscribers.Add(delta)
}

// --- Background loops ---

// Loop starts timing one run of a background loop; call the returned
// function with the run's error (nil: it succeeded).
func (m *Metrics) Loop(loop string) func(err error) {
	if m == nil {
		return func(error) {}
	}
	start := time.Now()
	return func(err error) {
		m.loopRuns.WithLabelValues(loop, okOrError(err)).Inc()
		m.loopDuration.WithLabelValues(loop).Observe(time.Since(start).Seconds())
		if err == nil {
			m.loopLast.WithLabelValues(loop).SetToCurrentTime()
		}
	}
}

// --- Label helpers ---

func counter(name, help string) prometheus.Counter {
	return prometheus.NewCounter(prometheus.CounterOpts{Namespace: "pdp", Name: name, Help: help})
}

func gauge(name, help string) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "pdp", Name: name, Help: help})
}

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "pdp", Name: name, Help: help}, labels)
}

func gaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "pdp", Name: name, Help: help}, labels)
}

func histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	return prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "pdp", Name: name, Help: help, Buckets: buckets}, labels)
}

func okOrError(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
