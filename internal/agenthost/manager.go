// Package agenthost runs agents for the platform: it wakes an agent's runtime
// on demand, relays requests to it, and hibernates it when idle.
//
// Authorization happens before anything here is called: callers pass only
// agents the actor may use. Runtimes are reached with their own bearer token
// and never see the caller's cookies or credentials.
package agenthost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agenthost/launcher"
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

	// Resolve returns an agent's current spec (used by the reaper to find a
	// runtime's token after a platform restart). Optional.
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

// Wake starts the agent if needed and returns its endpoint.
//
// A running agent costs one Lookup. Starting one happens under a database
// lock per agent, so two replicas waking it at the same moment start one
// runtime: the second waits, then finds it running. The launcher's Ensure is
// idempotent and runs under the lock, which is the re-check.
func (m *Manager) Wake(ctx context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	m.touch(spec.AgentID)
	if m.locker == nil {
		return m.launcher.Ensure(ctx, spec)
	}
	if ep, ok := m.lookup(ctx, spec); ok {
		return ep, nil
	}
	// Requests of this replica for the same agent queue here, so that only
	// one of them waits on the database lock.
	gate := m.gate(spec.AgentID)
	defer m.ungate(spec.AgentID, gate)
	select {
	case gate.ch <- struct{}{}:
		defer func() { <-gate.ch }()
	case <-ctx.Done():
		return launcher.Endpoint{}, ctx.Err()
	}
	if ep, ok := m.lookup(ctx, spec); ok { // a request ahead of us started it
		return ep, nil
	}
	lctx, cancel := context.WithTimeout(ctx, startLockWait)
	defer cancel()
	release, err := m.locker.Lock(lctx, wakeLockPrefix+spec.AgentID)
	if err != nil {
		return launcher.Endpoint{}, fmt.Errorf("wait to start the agent: %w", err)
	}
	defer release()
	return m.launcher.Ensure(ctx, spec)
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

// Hibernate stops the agent's runtime now.
func (m *Manager) Hibernate(ctx context.Context, agentID string) error {
	return m.launcher.Stop(ctx, agentID)
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
// Only the method, path, query, Content-Type and body are forwarded.
func (m *Manager) Proxy(w http.ResponseWriter, r *http.Request, spec launcher.Spec, path string) {
	ep, err := m.Wake(r.Context(), spec)
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
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			pr.Out.Host = target.Host
			contentType := pr.In.Header.Get("Content-Type")
			pr.Out.Header = http.Header{}
			if contentType != "" {
				pr.Out.Header.Set("Content-Type", contentType)
			}
			pr.Out.Header.Set("Authorization", "Bearer "+ep.Token)
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
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
			writeError(w, http.StatusBadGateway, "agent_unavailable", "The agent did not answer. Try again shortly.")
		},
	}
	rp.ServeHTTP(w, r)
}

func isEventStream(path string) bool {
	return len(path) > len("/events") && path[len(path)-len("/events"):] == "/events"
}

// runtimeStatus is what a runtime reports at /status. OpenStreams is nil for
// a runtime that predates it (an agent started before the platform was
// upgraded): the reaper then goes by this replica's streams alone.
type runtimeStatus struct {
	BusyThreads    int   `json:"busyThreads"`
	OpenStreams    *int  `json:"openStreams"`
	LastActivityAt int64 `json:"lastActivityAt"`
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
			m.log.Warn("reaper: take the lock", "err", err)
			return
		}
		if !ok {
			return // another replica is reaping
		}
		defer release()
	}
	running := m.launcher.Running()
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
		if err := m.launcher.Stop(ctx, agentID); err != nil {
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
			m.ReapOnce(ctx)
		}
	}
}

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
	var body struct{ Data runtimeStatus }
	err = json.NewDecoder(resp.Body).Decode(&body)
	return body.Data, err
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
