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

// Manager wakes, proxies to and hibernates agent runtimes.
type Manager struct {
	launcher launcher.Launcher
	idle     time.Duration
	log      *slog.Logger
	client   *http.Client

	// Resolve returns an agent's current spec (used by the reaper to find a
	// runtime's token after a platform restart). Optional.
	Resolve func(ctx context.Context, agentID string) (launcher.Spec, error)

	mu       sync.Mutex
	streams  map[string]int       // open event streams per agent
	lastUsed map[string]time.Time // last proxied request per agent

	// closing ends every open event stream when the platform shuts down
	// (clients reconnect to another replica, or after the restart).
	closing     chan struct{}
	closingOnce sync.Once
}

// CloseStreams ends every open event stream now (http.Server.RegisterOnShutdown).
func (m *Manager) CloseStreams() { m.closingOnce.Do(func() { close(m.closing) }) }

// New returns a manager. idle is how long an agent may sit with no work, no
// requests and no open streams before it hibernates.
func New(l launcher.Launcher, idle time.Duration, log *slog.Logger) *Manager {
	return &Manager{
		launcher: l, idle: idle, log: log,
		client:  &http.Client{Timeout: 10 * time.Second},
		streams: map[string]int{}, lastUsed: map[string]time.Time{},
		closing: make(chan struct{}),
	}
}

func (m *Manager) Launcher() launcher.Launcher { return m.launcher }

// Wake starts the agent if needed and returns its endpoint.
func (m *Manager) Wake(ctx context.Context, spec launcher.Spec) (launcher.Endpoint, error) {
	m.touch(spec.AgentID)
	return m.launcher.Ensure(ctx, spec)
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

type runtimeStatus struct {
	BusyThreads    int   `json:"busyThreads"`
	LastActivityAt int64 `json:"lastActivityAt"`
}

// ReapOnce hibernates every running agent that has been idle for longer than
// the idle timeout: no work in progress, no open streams, no recent requests.
func (m *Manager) ReapOnce(ctx context.Context) {
	for agentID, ep := range m.launcher.Running() {
		m.mu.Lock()
		streams, last := m.streams[agentID], m.lastUsed[agentID]
		m.mu.Unlock()
		if streams > 0 || time.Since(last) < m.idle {
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
		if st.BusyThreads > 0 || time.Since(time.UnixMilli(st.LastActivityAt)) < m.idle {
			continue
		}
		m.log.Info("hibernating idle agent", "agent", agentID)
		if err := m.launcher.Stop(ctx, agentID); err != nil {
			m.log.Warn("hibernate failed", "agent", agentID, "err", err)
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
