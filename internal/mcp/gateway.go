package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/netguard"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/tracing"
)

// Agent is an authenticated agent, as the gateway needs it.
type Agent struct {
	ID, TeamID, TemplateID uuid.UUID
	Version                int32
	// ClientMCP: its template version takes MCP servers from apps.
	ClientMCP bool
	// Credential is what the agent presents to the gateway.
	Credential string
}

// AgentAuth checks an agent's gateway credential.
type AgentAuth func(ctx context.Context, agentID uuid.UUID, token string) (Agent, error)

// RuntimeServer is how an agent's runtime reaches one MCP server (the
// runtime's MCP_SERVERS, and the gateway's /thread-mcp).
type RuntimeServer struct {
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	// Tools are the catalog server's tools the agent may use (absent for
	// client servers: the runtime lists them).
	Tools []Tool `json:"tools,omitempty"`
	// Thread and Client mark a server an app gave one thread.
	Thread string `json:"thread,omitempty"`
	Client bool   `json:"client,omitempty"`
	// SendThread: the runtime names the thread of each tool call
	// (mcpidentity.ThreadHeader), so the gateway can say who the call is
	// for. Only for catalog servers reached through the gateway; the
	// header never goes further.
	SendThread bool `json:"sendThread,omitempty"`
}

func proxyURL(gatewayURL, kind string, id uuid.UUID) string {
	return strings.TrimRight(gatewayURL, "/") + "/mcp-proxy/" + kind + "/" + id.String()
}

// TemplateServers are the MCP servers an agent's template version gives it,
// for its runtime. Servers that are disabled or no longer published to the
// team are left out (the agent starts without them).
func (s *Service) TemplateServers(ctx context.Context, ag Agent, gatewayURL string) ([]RuntimeServer, error) {
	rows, err := s.q.ListTemplateVersionMCP(ctx, dbgen.ListTemplateVersionMCPParams{TemplateID: ag.TemplateID, Version: ag.Version})
	if err != nil {
		return nil, err
	}
	out := []RuntimeServer{}
	for _, r := range rows {
		ok, err := s.q.MCPServerAvailable(ctx, dbgen.MCPServerAvailableParams{ServerID: r.ServerID, TeamID: ag.TeamID})
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		m, err := s.q.GetMCPServer(ctx, r.ServerID)
		if err != nil {
			return nil, err
		}
		var tools []Tool
		_ = json.Unmarshal(m.Tools, &tools)
		if r.Tools != nil {
			allowed := map[string]bool{}
			for _, t := range r.Tools {
				allowed[t] = true
			}
			kept := tools[:0]
			for _, t := range tools {
				if allowed[t.Name] {
					kept = append(kept, t)
				}
			}
			tools = kept
		}
		rs := RuntimeServer{Slug: m.Slug, Name: m.Name, Description: m.Description, Tools: tools}
		if m.AccessMode == ModeDirect {
			rs.URL = m.URL
			if rs.Headers, err = s.open(m.HeadersEnc, serverAD(m.ID)); err != nil {
				return nil, err
			}
		} else {
			rs.URL, rs.Headers = proxyURL(gatewayURL, "server", m.ID), map[string]string{"Authorization": "Bearer " + ag.Credential}
			rs.SendThread = true
		}
		out = append(out, rs)
	}
	return out, nil
}

// clientServers are a thread's client servers (every thread's with ""), for
// the agent's runtime.
func (s *Service) clientServers(ctx context.Context, ag Agent, threadID, gatewayURL string) ([]RuntimeServer, error) {
	out := []RuntimeServer{}
	if !ag.ClientMCP || len(s.Client.Hosts) == 0 {
		return out, nil
	}
	var t *string
	if threadID != "" {
		t = &threadID
	}
	rows, err := s.q.ListThreadMCPServers(ctx, dbgen.ListThreadMCPServersParams{AgentID: ag.ID, ThreadID: t})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		u, err := checkURL(r.URL)
		if err != nil || !s.Client.Allowed(u) {
			continue // the allowlist changed since
		}
		rs := RuntimeServer{Slug: r.Slug, Name: r.Name, Thread: r.ThreadID, Client: true}
		if s.Client.Mode == ModeDirect {
			rs.URL = r.URL
			if rs.Headers, err = s.open(r.HeadersEnc, threadAD(r.ID)); err != nil {
				return nil, err
			}
		} else {
			rs.URL, rs.Headers = proxyURL(gatewayURL, "thread", r.ID), map[string]string{"Authorization": "Bearer " + ag.Credential}
		}
		out = append(out, rs)
	}
	return out, nil
}

// RegisterGateway adds the MCP routes to the gateway agents call.
func (s *Service) RegisterGateway(mux *http.ServeMux, auth AgentAuth, gatewayURL string, credential func(*http.Request) (uuid.UUID, string, bool), log *slog.Logger) {
	authed := func(w http.ResponseWriter, r *http.Request) (Agent, bool) {
		id, token, ok := credential(r)
		if !ok {
			gatewayError(w, http.StatusUnauthorized, "unauthorized", "A gateway credential is required")
			return Agent{}, false
		}
		ag, err := auth(r.Context(), id, token)
		if err != nil {
			gatewayError(w, http.StatusUnauthorized, "unauthorized", "Invalid gateway credential")
			return Agent{}, false
		}
		return ag, true
	}
	mux.HandleFunc("GET /thread-mcp", func(w http.ResponseWriter, r *http.Request) {
		ag, ok := authed(w, r)
		if !ok {
			return
		}
		servers, err := s.clientServers(r.Context(), ag, r.URL.Query().Get("thread"), gatewayURL)
		if err != nil {
			log.Error("thread mcp servers", "agent", ag.ID, "err", err)
			gatewayError(w, http.StatusInternalServerError, "internal", "Internal error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": servers})
	})
	for _, kind := range []string{"server", "thread"} {
		mux.HandleFunc("/mcp-proxy/"+kind+"/{id}", func(w http.ResponseWriter, r *http.Request) {
			ag, ok := authed(w, r)
			if !ok {
				return
			}
			s.proxy(w, r, ag, kind, log)
		})
	}
}

func gatewayError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// target is the upstream an agent may reach through the proxy.
type target struct {
	url        string
	headers    map[string]string
	allowed    map[string]bool // nil: every tool
	serverID   uuid.NullUUID
	serverName string
	client     bool
	// team: a team's own server, reached through the address guard.
	team bool
	// metric is the server's label in the metrics and traces: the slug of
	// a platform server, metrics.ServerTeam or metrics.ServerApp.
	metric string
	// thread is the thread an app's server belongs to ("" for others).
	thread string
	// identity: send the identity token (a platform or team server in
	// proxy mode with Send identity on; never an app's), for audience (its
	// URL when ""), with the person's email when email.
	identity bool
	audience string
	email    bool
}

func (s *Service) resolveTarget(ctx context.Context, ag Agent, kind string, id uuid.UUID) (target, bool, error) {
	if kind == "thread" {
		r, err := s.q.GetThreadMCPServer(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return target{}, false, nil
		} else if err != nil {
			return target{}, false, err
		}
		u, uerr := checkURL(r.URL)
		if r.AgentID != ag.ID || !ag.ClientMCP || time.Now().After(r.ExpiresAt) || uerr != nil || !s.Client.Allowed(u) {
			return target{}, false, nil
		}
		headers, err := s.open(r.HeadersEnc, threadAD(r.ID))
		return target{url: r.URL, headers: headers, serverName: clientServerName(r.Name), client: true, metric: metrics.ServerApp,
			thread: r.ThreadID}, true, err
	}
	vm, err := s.q.GetTemplateVersionMCP(ctx, dbgen.GetTemplateVersionMCPParams{TemplateID: ag.TemplateID, Version: ag.Version, ServerID: id})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return target{}, false, nil
	} else if err != nil {
		return target{}, false, err
	}
	ok, err := s.q.MCPServerAvailable(ctx, dbgen.MCPServerAvailableParams{ServerID: id, TeamID: ag.TeamID})
	if err != nil || !ok {
		return target{}, false, err
	}
	m, err := s.q.GetMCPServer(ctx, id)
	if err != nil {
		return target{}, false, err
	}
	headers, err := s.open(m.HeadersEnc, serverAD(m.ID))
	t := target{url: m.URL, headers: headers, serverID: uuid.NullUUID{UUID: m.ID, Valid: true}, serverName: m.Name, team: m.TeamID.Valid, metric: m.Slug,
		identity: m.SendIdentity && m.AccessMode == ModeProxy, audience: m.IdentityAudience, email: m.IdentityEmail}
	if m.TeamID.Valid {
		t.metric = metrics.ServerTeam
	}
	if vm.Tools != nil {
		t.allowed = map[string]bool{}
		for _, name := range vm.Tools {
			t.allowed[name] = true
		}
	}
	return t, true, err
}

const maxMCPRequest = 4 << 20

type rpcCall struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

// proxy counts and times the request (by server once it is known).
func (s *Service) proxy(w http.ResponseWriter, r *http.Request, ag Agent, kind string, log *slog.Logger) {
	began := time.Now()
	sw := httpx.NewStatusWriter(w)
	server := ""
	defer func() { s.Metrics.MCPRequest(server, sw.Status(), time.Since(began)) }()
	s.relay(sw, r, ag, kind, log, func(label string) { server = label })
}

func (s *Service) relay(w http.ResponseWriter, r *http.Request, ag Agent, kind string, log *slog.Logger, known func(server string)) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		gatewayError(w, http.StatusNotFound, "not_found", "Unknown MCP server")
		return
	}
	t, ok, err := s.resolveTarget(ctx, ag, kind, id)
	if err != nil {
		log.Error("mcp proxy: resolve", "agent", ag.ID, "err", err)
		gatewayError(w, http.StatusInternalServerError, "internal", "Internal error")
		return
	}
	if !ok {
		gatewayError(w, http.StatusForbidden, "mcp_server_not_allowed", "This agent may not use that MCP server")
		return
	}
	known(t.metric)
	// The thread the runtime says the request belongs to: only a name to
	// look up among this agent's threads (identity.go).
	thread := r.Header.Get(mcpidentity.ThreadHeader)
	if thread != "" && !threadIDRE.MatchString(thread) {
		gatewayError(w, http.StatusBadRequest, "invalid_thread", "The "+mcpidentity.ThreadHeader+" header must be a thread ID")
		return
	}
	var body []byte
	var calls []string
	single := false
	if r.Method == http.MethodPost {
		if body, err = io.ReadAll(io.LimitReader(r.Body, maxMCPRequest+1)); err != nil || len(body) > maxMCPRequest {
			gatewayError(w, http.StatusRequestEntityTooLarge, "too_large", "The request is too large")
			return
		}
		var msgs []rpcCall
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			err = json.Unmarshal(trimmed, &msgs)
		} else {
			var one rpcCall
			err = json.Unmarshal(trimmed, &one)
			msgs = []rpcCall{one}
		}
		if err != nil {
			gatewayError(w, http.StatusBadRequest, "invalid_request", "The body must be JSON-RPC")
			return
		}
		single = len(msgs) == 1
		for _, m := range msgs {
			if m.Method != "tools/call" {
				continue
			}
			if t.allowed != nil && !t.allowed[m.Params.Name] {
				s.Metrics.MCPToolCalls(t.metric, "denied", 1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
					"error": map[string]any{"code": -32602, "message": "This agent may not use the tool " + m.Params.Name}})
				return
			}
			calls = append(calls, m.Params.Name)
		}
	} else if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		gatewayError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST, GET or DELETE")
		return
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, t.url, bytes.NewReader(body))
	if err != nil {
		gatewayError(w, http.StatusInternalServerError, "internal", "Internal error")
		return
	}
	for k, v := range t.headers {
		up.Header.Set(k, v)
	}
	// Only the gateway's own token travels in the identity header.
	up.Header.Del(mcpidentity.Header)
	if t.identity && s.Identity != nil {
		tool := ""
		if single && len(calls) == 1 {
			tool = calls[0]
		}
		token, err := s.identityToken(ctx, ag, t, thread, tool)
		if err != nil {
			// Never the token or key material: the error says which step failed.
			log.Error("mcp proxy: identity token", "agent", ag.ID, "server", t.serverName, "err", err)
			gatewayError(w, http.StatusServiceUnavailable, "identity_unavailable", "The platform couldn't sign the identity token for this MCP server")
			s.logCalls(ag, t, calls, false, time.Now(), log)
			return
		}
		up.Header.Set(mcpidentity.Header, token)
	}
	for _, h := range []string{"Content-Type", "Accept", "Mcp-Session-Id", "MCP-Protocol-Version", "Last-Event-ID"} {
		if v := r.Header.Get(h); v != "" {
			up.Header.Set(h, v)
		}
	}
	started := time.Now()
	client := s.client
	if t.team {
		client = s.team.Client()
	}
	// The upstream call is a span of its own (the server's label and the
	// names of the tools called, never arguments, headers or the URL), and
	// carries the trace context to the server.
	spanAttrs := []attribute.KeyValue{attribute.String("pdp.mcp.server", t.metric), attribute.Int("pdp.mcp.tool_calls", len(calls))}
	if len(calls) > 0 {
		spanAttrs = append(spanAttrs, attribute.StringSlice("pdp.mcp.tools", spanTools(calls)))
	}
	uctx, span := tracing.Start(ctx, "mcp.upstream", trace.SpanKindClient, spanAttrs...)
	defer span.End()
	up = up.WithContext(uctx)
	tracing.Inject(uctx, up.Header)
	resp, err := client.Do(up)
	if err != nil {
		tracing.Fail(span, "upstream unreachable")
		if errors.Is(err, netguard.ErrBlockedAddress) {
			// A team server's host now resolves outside what teams may reach.
			log.Warn("mcp proxy: team server address not allowed", "server", t.serverName, "err", errors.Unwrap(err))
			gatewayError(w, http.StatusBadGateway, "address_not_allowed", "The MCP server's address is on a network teams can't use")
			s.logCalls(ag, t, calls, false, started, log)
			return
		}
		if ctx.Err() == nil {
			log.Warn("mcp proxy: upstream unreachable", "server", t.serverName, "err", errors.Unwrap(err))
		}
		gatewayError(w, http.StatusBadGateway, "upstream_unreachable", "The MCP server could not be reached")
		s.logCalls(ag, t, calls, false, started, log)
		return
	}
	defer resp.Body.Close()
	tracing.Status(span, resp.StatusCode, false)
	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "content-encoding", "set-cookie", "connection", "keep-alive", "transfer-encoding", "trailer", "upgrade":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	s.logCalls(ag, t, calls, resp.StatusCode < 300, started, log)
}

func (s *Service) logCalls(ag Agent, t target, calls []string, ok bool, started time.Time, log *slog.Logger) {
	if len(calls) == 0 {
		return
	}
	result := "ok"
	if !ok {
		result = "error"
	}
	s.Metrics.MCPToolCalls(t.metric, result, len(calls))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The connected app the calls count for (none unless unambiguous; a
	// thread's own server looks at its thread only).
	grant := oauth.Attribution(ctx, s.q, ag.ID, t.thread)
	for _, tool := range calls {
		if err := s.q.InsertMCPCall(ctx, dbgen.InsertMCPCallParams{TeamID: ag.TeamID, AgentID: uuid.NullUUID{UUID: ag.ID, Valid: true},
			ServerID: t.serverID, ServerName: t.serverName, Client: t.client, Tool: truncate(tool, 128), Ok: ok,
			DurationMs: int32(time.Since(started).Milliseconds()), OauthGrantID: grant}); err != nil {
			log.Warn("mcp proxy: log call", "err", err)
		}
	}
}

// spanTools are the tool names of a call for a span: truncated, and at most 16.
func spanTools(calls []string) []string {
	out := make([]string, 0, min(len(calls), 16))
	for _, c := range calls[:min(len(calls), 16)] {
		out = append(out, truncate(c, 128))
	}
	return out
}
