// Package agenthost runs agents for the platform: it wakes an agent's runtime
// on demand, relays requests to it, and hibernates it when idle.
//
// Authorization happens before anything here is called: callers pass only
// agents the actor may use. Runtimes are reached with their own bearer token
// and never see the caller's cookies or credentials.
package agenthost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/tracing"
)

// Locker takes named locks shared by every platform replica (PostgreSQL
// advisory locks; store.Locks). Held locks end with their holder: a replica
// that dies releases them.
type Locker interface {
	// Lock waits for the lock until ctx ends.
	Lock(ctx context.Context, key string) (release func(), err error)
	// TryLock takes the lock when nobody holds it.
	TryLock(ctx context.Context, key string) (release func(), ok bool, err error)
}

// ErrDisabled: the agent was turned off after the request that wakes it was
// authorized; it isn't started.
var ErrDisabled = errors.New("the agent is turned off")

// RefusedError: the platform refuses to run the agent on the runtime it
// started (the upgrade check: the runtime is older than the one that last
// wrote the agent's data). Message says why, for people.
type RefusedError struct{ Message string }

func (e *RefusedError) Error() string { return e.Message }

const (
	// wakeLockPrefix + the agent's ID names the lock that serialises starting
	// that agent across replicas.
	wakeLockPrefix = "pdp.agent-wake:"
	// reapLockKey names the lock of the replica that reaps idle agents now.
	reapLockKey = "pdp.agent-reaper"
	// startLockWait bounds how long a wake waits for another replica's start
	// of the same agent (longer than a launcher's own start timeouts).
	startLockWait = 3 * time.Minute
)

// Manager wakes, proxies to and hibernates agent runtimes.
//
// Several platform replicas may run one Manager each against the same
// launcher backend (the Kubernetes API) and database. Nothing a Manager
// keeps decides anything the others could contradict: starting an agent is
// serialised by a database lock and re-checked under it, and the reaper asks
// the runtime itself whether the agent is idle (its work, its open streams
// and its last request, wherever they came from) instead of trusting what
// this replica has seen. The in-process records below are only hints that
// save calls.
type Manager struct {
	launcher launcher.Launcher
	idle     time.Duration
	log      *slog.Logger
	client   *http.Client
	locker   Locker

	// Metrics counts wakes, hibernations and the agents running (nil: none).
	Metrics *metrics.Metrics

	// Admit is the upgrade check after a start (optional): given the
	// pi-durable version the new runtime reports at /status ("" when it
	// reports none), it records it or refuses it with a *RefusedError. A
	// refused runtime is stopped and the wake fails with the refusal. The
	// runtime asks the platform the same before it opens the agent's storage;
	// this covers a runtime that didn't.
	Admit func(ctx context.Context, agentID, piDurableVersion string) error

	// Resolve returns an agent's current spec, from the database: what a start
	// runs (under the agent's start lock), and how the reaper finds a
	// runtime's token after a platform restart. Optional (tests): without it a
	// start runs the caller's spec.
	Resolve func(ctx context.Context, agentID string) (launcher.Spec, error)

	mu        sync.Mutex
	streams   map[string]int       // open event streams per agent (on this replica)
	lastUsed  map[string]time.Time // last request proxied by this replica, per agent
	skipUntil map[string]time.Time // the reaper need not ask the runtime again before this
	starting  map[string]*startGate

	// closing ends every open event stream when the platform shuts down
	// (clients reconnect to another replica, or after the restart).
	closing     chan struct{}
	closingOnce sync.Once
}

// CloseStreams ends every open event stream now (http.Server.RegisterOnShutdown).
func (m *Manager) CloseStreams() { m.closingOnce.Do(func() { close(m.closing) }) }

// Closing is closed when the platform shuts down (other event streams end then too).
func (m *Manager) Closing() <-chan struct{} { return m.closing }

// New returns a manager. idle is how long an agent may sit with no work, no
// requests and no open streams before it hibernates.
func New(l launcher.Launcher, idle time.Duration, log *slog.Logger) *Manager {
	return &Manager{
		launcher: l, idle: idle, log: log,
		client:  &http.Client{Timeout: 10 * time.Second},
		streams: map[string]int{}, lastUsed: map[string]time.Time{}, skipUntil: map[string]time.Time{},
		starting: map[string]*startGate{},
		closing:  make(chan struct{}),
	}
}

// SetLocker makes starts and reaping safe with several replicas. Without a
// locker (tests, a single process that wants none) starts and reaping are
// not coordinated with other processes.
func (m *Manager) SetLocker(l Locker) { m.locker = l }

func (m *Manager) Launcher() launcher.Launcher { return m.launcher }

// Wake starts the agent if needed and returns its endpoint. The launcher's
// mode, how the runtime was obtained and the result are counted and timed
// (the fast path of an agent that is already running too: its start is
// "running").
func (m *Manager) Wake(ctx context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	m.touch(spec.AgentID)
	kind := m.launcher.Kind()
	ctx, span := tracing.Start(ctx, "agent.wake", trace.SpanKindInternal,
		attribute.String("pdp.launcher", metrics.LauncherMode(kind)), attribute.String("pdp.agent.id", spec.AgentID))
	defer span.End()
	ctx, outcome := launcher.WithOutcome(ctx)
	began := time.Now()
	ep, err := m.wake(ctx, spec)
	start, result := outcome.Start(), wakeResult(err)
	if start == "" {
		start = launcher.StartRunning
		if err != nil {
			start = "unknown"
		}
	}
	span.SetAttributes(attribute.String("pdp.wake.start", start), attribute.String("pdp.wake.result", result))
	if err != nil {
		tracing.Fail(span, result)
	}
	m.Metrics.Wake(kind, start, result, time.Since(began))
	return ep, err
}

func wakeResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "error"
}

// wake is Wake without the instrumentation.
//
// A running agent costs one Lookup. Starting one happens under the agent's
// start lock (startLock: this process's gate, then a database lock shared by
// replicas), so two replicas waking it at the same moment start one runtime:
// the second waits, then finds it running. The launcher's Ensure is
// idempotent and runs under the lock, which is the re-check.
//
// The runtime starts with the agent's spec as Resolve finds it under the
// lock, not with the caller's: the caller built its spec when it authorized
// the request, and the agent may have changed since (a move to another team,
// whose stop takes the same lock: Hibernate). A start therefore never brings
// back the configuration a change stopped.
func (m *Manager) wake(ctx context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	if ep, ok := m.lookup(ctx, spec); ok {
		return ep, nil
	}
	var ep launcher.Endpoint
	started := func() (ok bool) { // a request ahead of us started it
		ep, ok = m.lookup(ctx, spec)
		return ok
	}
	release, err := m.startLock(ctx, spec.AgentID, started)
	if err != nil || release == nil {
		return ep, err
	}
	defer release()
	if m.Resolve != nil {
		if spec, err = m.Resolve(ctx, spec.AgentID); err != nil {
			return launcher.Endpoint{}, fmt.Errorf("the agent's current spec: %w", err)
		}
	}
	if spec.Disabled {
		return launcher.Endpoint{}, ErrDisabled
	}
	ep, err = m.launcher.Ensure(ctx, spec)
	if err != nil || m.Admit == nil {
		return ep, err
	}
	if err := m.admit(ctx, spec.AgentID, ep); err != nil {
		return launcher.Endpoint{}, err
	}
	return ep, nil
}

// admit runs the upgrade check on a runtime that was just started (under the
// agent's start lock): it reads the runtime's versions at /status and asks
// Admit. A refused runtime is stopped. A runtime whose /status can't be read
// is let through (the next request finds out whether it works).
func (m *Manager) admit(ctx context.Context, agentID string, ep launcher.Endpoint) error {
	st, err := m.status(ctx, ep)
	if err != nil {
		m.log.Warn("upgrade check: the runtime's status", "agent", agentID, "err", err)
		return nil
	}
	err = m.Admit(ctx, agentID, st.PiDurableVersion)
	var refused *RefusedError
	if !errors.As(err, &refused) {
		if err != nil {
			m.log.Warn("upgrade check", "agent", agentID, "err", err)
		}
		return nil
	}
	m.log.Warn("refused to start the agent on an older runtime", "agent", agentID, "runtime_pi_durable", st.PiDurableVersion)
	if stopErr := m.launcher.Stop(context.WithoutCancel(ctx), agentID); stopErr != nil {
		m.log.Warn("stop a refused runtime", "agent", agentID, "err", stopErr)
	}
	return refused
}

// startLock takes the agent's start lock and returns its release. Requests of
// this process for the same agent queue on its gate, so that only one of them
// waits on the database lock (taken when there is a locker: several
// replicas). done (optional) is asked once this request's turn has come: when
// it says the work is done (a request ahead of this one started the agent),
// nothing is held and release is nil.
func (m *Manager) startLock(ctx context.Context, agentID string, done func() bool) (release func(), err error) {
	gate := m.gate(agentID)
	select {
	case gate.ch <- struct{}{}:
	case <-ctx.Done():
		m.ungate(agentID, gate)
		return nil, ctx.Err()
	}
	unGate := func() {
		<-gate.ch
		m.ungate(agentID, gate)
	}
	if done != nil && done() {
		unGate()
		return nil, nil
	}
	if m.locker == nil {
		return unGate, nil
	}
	lctx, cancel := context.WithTimeout(ctx, startLockWait)
	defer cancel()
	unlock, err := m.locker.Lock(lctx, wakeLockPrefix+agentID)
	if err != nil {
		unGate()
		return nil, fmt.Errorf("wait for the agent's start lock: %w", err)
	}
	return func() { unlock(); unGate() }, nil
}

// lookup is the launcher's cheap check; an error counts as "not running" and
// is left to Ensure to report.
func (m *Manager) lookup(ctx context.Context, spec launcher.Spec) (launcher.Endpoint, bool) {
	ep, ok, err := m.launcher.Lookup(ctx, spec)
	return ep, ok && err == nil
}

// startGate lets one request at a time of this process start an agent.
type startGate struct {
	ch   chan struct{}
	refs int
}

func (m *Manager) gate(agentID string) *startGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	g := m.starting[agentID]
	if g == nil {
		g = &startGate{ch: make(chan struct{}, 1)}
		m.starting[agentID] = g
	}
	g.refs++
	return g
}

func (m *Manager) ungate(agentID string, g *startGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g.refs--; g.refs == 0 {
		delete(m.starting, agentID)
	}
}

// Hibernate stops the agent's runtime now (on request: an explicit hibernate,
// or a change to the agent that restarts it). It takes the agent's start lock,
// so a start under way (which may have resolved the agent before the change)
// finishes first and is stopped too, and a start after it resolves the agent
// as changed.
func (m *Manager) Hibernate(ctx context.Context, agentID string) error {
	release, err := m.startLock(ctx, agentID, nil)
	if err == nil {
		err = m.launcher.Stop(ctx, agentID)
		release()
	}
	m.Metrics.Hibernation("request", err)
	return err
}

// Remove stops the agent and deletes what its launcher keeps for it.
func (m *Manager) Remove(ctx context.Context, agentID string) error {
	m.mu.Lock()
	delete(m.lastUsed, agentID)
	delete(m.streams, agentID)
	delete(m.skipUntil, agentID)
	m.mu.Unlock()
	return m.launcher.Remove(ctx, agentID)
}

// Running reports whether the agent has a live runtime.
func (m *Manager) Running(agentID string) bool {
	_, ok := m.launcher.Running()[agentID]
	return ok
}

func (m *Manager) touch(agentID string) {
	m.mu.Lock()
	m.lastUsed[agentID] = time.Now()
	m.mu.Unlock()
}

// Proxy relays the request to path on the agent's runtime (waking it first)
// and streams the response back, flushing as it arrives (server-sent events).
// Only the method, path, query, Content-Type, Last-Event-ID and body are
// forwarded.
func (m *Manager) Proxy(w http.ResponseWriter, r *http.Request, spec launcher.Spec, path string) {
	ep, err := m.Wake(r.Context(), spec)
	if errors.Is(err, ErrDisabled) {
		writeError(w, http.StatusConflict, "agent_disabled", "This agent is turned off")
		return
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		writeError(w, http.StatusConflict, "runtime_too_old", refused.Message)
		return
	}
	if err != nil {
		m.log.Warn("agent wake failed", "agent", spec.AgentID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "agent_unavailable", "The agent could not be started. Try again shortly.")
		return
	}
	target, err := url.Parse(ep.BaseURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "Internal error")
		return
	}
	stream := r.Method == http.MethodGet && isEventStream(path)
	if stream {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			select {
			case <-m.closing:
				cancel()
			case <-ctx.Done():
			}
		}()
		r = r.WithContext(ctx)
		m.mu.Lock()
		m.streams[spec.AgentID]++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.streams[spec.AgentID]--
			m.lastUsed[spec.AgentID] = time.Now()
			m.mu.Unlock()
		}()
	}
	// The relay to the runtime is a span of its own; its trace context goes
	// to the runtime (traceparent), so a request can be followed from the
	// API to the runtime if the runtime logs or continues it.
	ctx, span := tracing.Start(r.Context(), "agent.runtime_request", trace.SpanKindClient,
		attribute.String("http.request.method", metrics.Method(r.Method)), attribute.String("pdp.agent.id", spec.AgentID))
	defer span.End()
	r = r.WithContext(ctx)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = target.Host
			contentType, lastEvent := pr.In.Header.Get("Content-Type"), pr.In.Header.Get("Last-Event-ID")
			pr.Out.Header = http.Header{}
			if contentType != "" {
				pr.Out.Header.Set("Content-Type", contentType)
			}
			// A reconnecting event stream's position (the runtime numbers delta events after it).
			if lastEvent != "" && len(lastEvent) <= 32 {
				pr.Out.Header.Set("Last-Event-ID", lastEvent)
			}
			pr.Out.Header.Set("Authorization", "Bearer "+ep.Token)
			tracing.Inject(pr.Out.Context(), pr.Out.Header)
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			tracing.Status(span, resp.StatusCode, false)
			for _, h := range []string{"Set-Cookie", "Server", "X-Powered-By"} {
				resp.Header.Del(h)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				return
			}
			m.log.Warn("agent proxy failed", "agent", spec.AgentID, "err", err)
			tracing.Fail(span, "runtime unreachable")
			writeError(w, http.StatusBadGateway, "agent_unavailable", "The agent did not answer. Try again shortly.")
		},
	}
	rp.ServeHTTP(w, r)
}

// maxCallBytes bounds what Call reads of a runtime's reply (a thread's last
// messages, a status).
const maxCallBytes = 4 << 20

// Call wakes the agent if needed and sends one request to path on its
// runtime (body, if any, as JSON), for the platform's own work (triggered
// runs), not a client's. It returns the status and at most 4 MiB of the
// reply; an error means the agent could not be started or did not answer.
func (m *Manager) Call(ctx context.Context, spec launcher.Spec, method, path string, body []byte) (int, []byte, error) {
	ep, err := m.Wake(ctx, spec)
	if err != nil {
		return 0, nil, err
	}
	ctx, span := tracing.Start(ctx, "agent.runtime_request", trace.SpanKindClient,
		attribute.String("http.request.method", metrics.Method(method)), attribute.String("pdp.agent.id", spec.AgentID))
	defer span.End()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(ep.BaseURL, "/")+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	tracing.Inject(ctx, req.Header)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		tracing.Fail(span, "runtime unreachable")
		return 0, nil, err
	}
	defer resp.Body.Close()
	tracing.Status(span, resp.StatusCode, false)
	m.touch(spec.AgentID)
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCallBytes))
	return resp.StatusCode, raw, err
}

func isEventStream(path string) bool {
	return len(path) > len("/events") && path[len(path)-len("/events"):] == "/events"
}

// runtimeStatus is what a runtime reports at /status (docs/runtime-contract.md).
// OpenStreams is nil for a runtime that predates it (an agent started before
// the platform was upgraded): the reaper then goes by this replica's streams
// alone. ContractVersion and PiDurableVersion are nil and empty for a runtime
// before contract version 1. PiDurableVersion is the upgrade check's input
// after a start (Admit).
type runtimeStatus struct {
	BusyThreads      int
	OpenStreams      *int
	LastActivityAt   int64
	ContractVersion  *int
	PiDurableVersion string
}

// maxContractVersion and piDurableVersionPattern bound what a runtime may
// report about its versions.
const maxContractVersion = 1000

var piDurableVersionPattern = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(-[0-9A-Za-z.-]{1,32})?$`)

// ValidPiDurableVersion reports whether a pi-durable version a runtime
// reported is one the platform accepts: a semantic version with an optional
// short pre-release.
func ValidPiDurableVersion(v string) bool { return piDurableVersionPattern.MatchString(v) }

// UnmarshalJSON reads the activity fields strictly (a malformed one is an
// error, as before) and the version fields leniently: the runtime is not
// trusted, and a version the platform would not accept (a contract version
// outside 1..maxContractVersion, a pi-durable version that is not a short
// semantic version, or a value of another type) is dropped rather than making
// the whole report unreadable, which would keep the reaper from hibernating
// the agent.
func (s *runtimeStatus) UnmarshalJSON(raw []byte) error {
	var wire struct {
		BusyThreads      int             `json:"busyThreads"`
		OpenStreams      *int            `json:"openStreams"`
		LastActivityAt   int64           `json:"lastActivityAt"`
		ContractVersion  json.RawMessage `json:"contractVersion"`
		PiDurableVersion json.RawMessage `json:"piDurableVersion"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*s = runtimeStatus{BusyThreads: wire.BusyThreads, OpenStreams: wire.OpenStreams, LastActivityAt: wire.LastActivityAt}
	var contract int
	if json.Unmarshal(wire.ContractVersion, &contract) == nil && contract >= 1 && contract <= maxContractVersion {
		s.ContractVersion = &contract
	}
	var pi string
	if json.Unmarshal(wire.PiDurableVersion, &pi) == nil && piDurableVersionPattern.MatchString(pi) {
		s.PiDurableVersion = pi
	}
	return nil
}

// ReapOnce hibernates every running agent that has been idle for longer than
// the idle timeout: no work in progress, no open streams, no recent requests.
//
// Only one replica reaps at a time (a database lock taken for the pass; the
// others skip it). The decision rests on the runtime's own report, which
// covers requests and streams that went through any replica, so which
// replica is elected does not matter and a replica that has seen nothing of
// an agent does not hibernate it by mistake. What this replica has seen only
// saves calls.
func (m *Manager) ReapOnce(ctx context.Context) {
	if m.locker != nil {
		release, ok, err := m.locker.TryLock(ctx, reapLockKey)
		if err != nil {
			if ctx.Err() == nil {
				m.log.Warn("reaper: take the lock", "err", err)
			}
			return
		}
		if !ok {
			return // another replica is reaping
		}
		defer release()
	}
	running := m.launcher.Running()
	m.Metrics.SetAgentsRunning(len(running))
	m.forgetStopped(running)
	for agentID, ep := range running {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		streams, last, skip := m.streams[agentID], m.lastUsed[agentID], m.skipUntil[agentID]
		m.mu.Unlock()
		if streams > 0 || time.Since(last) < m.idle || time.Now().Before(skip) {
			continue
		}
		if ep.Token == "" && m.Resolve != nil {
			spec, err := m.Resolve(ctx, agentID)
			if err != nil {
				m.log.Debug("resolve agent for reaping", "agent", agentID, "err", err)
				continue
			}
			ep.Token = spec.Token
		}
		st, err := m.status(ctx, ep)
		if err != nil {
			m.log.Debug("agent status failed", "agent", agentID, "err", err)
			continue
		}
		lastActive := time.UnixMilli(st.LastActivityAt)
		open := st.OpenStreams != nil && *st.OpenStreams > 0
		if st.BusyThreads > 0 || open || time.Since(lastActive) < m.idle {
			// Not before the agent could be idle (its last activity plus the
			// timeout), and while it works or is watched, not for half a
			// timeout: it counts as active until it stops, and then the
			// timeout starts.
			wait := m.idle - time.Since(lastActive)
			if st.BusyThreads > 0 || open {
				wait = m.idle / 2
			}
			m.mu.Lock()
			m.skipUntil[agentID] = time.Now().Add(max(wait, 0))
			m.mu.Unlock()
			continue
		}
		m.log.Info("hibernating idle agent", "agent", agentID)
		err = m.launcher.Stop(ctx, agentID)
		m.Metrics.Hibernation("idle", err)
		if err != nil {
			m.log.Warn("hibernate failed", "agent", agentID, "err", err)
		}
	}
}

// forgetStopped drops the reaper's hints about agents no longer running.
func (m *Manager) forgetStopped(running map[string]launcher.Endpoint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.skipUntil {
		if _, ok := running[id]; !ok {
			delete(m.skipUntil, id)
		}
	}
}

// ReapLoop runs ReapOnce every interval until ctx ends.
func (m *Manager) ReapLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			done := m.Metrics.Loop("reaper")
			m.ReapOnce(ctx)
			done(nil)
		}
	}
}

// maxStatusBytes bounds what the reaper reads from a runtime's /status (a few
// hundred bytes in practice). The runtime is not trusted: an agent's code can
// make it answer with any body, and the reaper runs on every replica.
const maxStatusBytes = 64 << 10

var errStatusTooLarge = errors.New("runtime status reply is too large")

func (m *Manager) status(ctx context.Context, ep launcher.Endpoint) (runtimeStatus, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ep.BaseURL+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	resp, err := m.client.Do(req)
	if err != nil {
		return runtimeStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runtimeStatus{}, fmt.Errorf("status %d", resp.StatusCode)
	}
	// Over the limit is an error, never a truncated parse.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBytes+1))
	if err != nil {
		return runtimeStatus{}, err
	}
	if len(raw) > maxStatusBytes {
		return runtimeStatus{}, errStatusTooLarge
	}
	var body struct{ Data runtimeStatus }
	err = json.Unmarshal(raw, &body)
	return body.Data, err
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
