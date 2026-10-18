package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
)

// The model proxy: agents of a proxy-mode provider call
//
//	{AGENTS_GATEWAY_URL}/model-proxy/{providerId}{path}
//
// with their credential (ProxyCredential) as the API key, in the provider's
// own API (the runtime's SDK appends path: /chat/completions, /responses or
// /v1/messages). The gateway checks the agent may use the model the request
// names, relays the request with the provider's key, streams the reply back
// and records the tokens it reports.

// ProxyCredentialPrefix starts an agent's model-proxy credential.
const ProxyCredentialPrefix = "pdpm."

// ProxyCredential is the API key an agent presents to the model proxy: its
// ID and its runtime token (which only the agent and the platform know).
func ProxyCredential(agentID, runtimeToken string) string {
	return ProxyCredentialPrefix + agentID + "." + runtimeToken
}

// ProxyBaseURL is the base URL an agent uses for a proxy-mode provider.
func ProxyBaseURL(gatewayURL string, providerID uuid.UUID) string {
	return strings.TrimRight(gatewayURL, "/") + "/model-proxy/" + providerID.String()
}

// AgentAuth checks an agent's credential and returns its team and the model
// it runs on (uuid.Nil: none from the catalog).
type AgentAuth func(ctx context.Context, agentID uuid.UUID, token string) (teamID, modelID uuid.UUID, err error)

// maxRequestBody bounds a relayed request (images are inlined as base64).
const maxRequestBody = 32 << 20

// Gateway returns the handler agents call.
func (s *Service) Gateway(auth AgentAuth, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"status":"ok"}}`)
	})
	mux.HandleFunc("POST /model-proxy/{providerId}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		s.proxy(w, r, auth, log)
	})
	return mux
}

func proxyError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": code, "code": code, "message": message}})
}

// credential reads the agent's credential from either auth header style.
func credential(r *http.Request) (uuid.UUID, string, bool) {
	raw := r.Header.Get("x-api-key")
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		raw = v
	}
	rest, ok := strings.CutPrefix(raw, ProxyCredentialPrefix)
	if !ok {
		return uuid.Nil, "", false
	}
	idPart, token, ok := strings.Cut(rest, ".")
	id, err := uuid.Parse(idPart)
	if !ok || err != nil || token == "" {
		return uuid.Nil, "", false
	}
	return id, token, true
}

func (s *Service) proxy(w http.ResponseWriter, r *http.Request, auth AgentAuth, log *slog.Logger) {
	ctx := r.Context()
	providerID, err := uuid.Parse(r.PathValue("providerId"))
	if err != nil {
		proxyError(w, http.StatusNotFound, "not_found", "Unknown provider")
		return
	}
	agentID, token, ok := credential(r)
	if !ok {
		proxyError(w, http.StatusUnauthorized, "unauthorized", "A model-proxy credential is required")
		return
	}
	teamID, modelID, err := auth(ctx, agentID, token)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusNotFound {
			proxyError(w, ae.Status, ae.Code, ae.Message)
			return
		}
		proxyError(w, http.StatusUnauthorized, "unauthorized", "Invalid model-proxy credential")
		return
	}
	if modelID == uuid.Nil {
		proxyError(w, http.StatusForbidden, "model_not_allowed", "This agent has no model from the catalog")
		return
	}
	m, err := s.Resolve(ctx, modelID, teamID)
	if errors.Is(err, ErrUnavailable) {
		proxyError(w, http.StatusForbidden, "model_unavailable", "The agent's model is disabled or not published to its team")
		return
	} else if err != nil {
		log.Error("model proxy: resolve", "agent", agentID, "err", err)
		proxyError(w, http.StatusInternalServerError, "internal", "Internal error")
		return
	}
	path := "/" + r.PathValue("path")
	if m.Provider.ID != providerID || !slices.Contains(m.Kind.ProxyPaths, path) {
		proxyError(w, http.StatusForbidden, "model_not_allowed", "This agent may not call that provider or endpoint")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		proxyError(w, http.StatusBadRequest, "invalid_request", "The request body could not be read")
		return
	}
	if len(body) > maxRequestBody {
		proxyError(w, http.StatusRequestEntityTooLarge, "too_large", "The request is too large")
		return
	}
	var named struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &named); err != nil {
		proxyError(w, http.StatusBadRequest, "invalid_request", "The request body must be JSON")
		return
	}
	if named.Model != m.Model.UpstreamID {
		proxyError(w, http.StatusForbidden, "model_not_allowed", "This agent may only use its own model")
		return
	}

	target := m.Provider.BaseURL + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		proxyError(w, http.StatusInternalServerError, "internal", "Internal error")
		return
	}
	up.Header.Set("Content-Type", "application/json")
	for _, h := range []string{"Accept", "anthropic-version", "anthropic-beta", "OpenAI-Beta"} {
		if v := r.Header.Get(h); v != "" {
			up.Header.Set(h, v)
		}
	}
	authenticate(up, m.Kind, m.APIKey, m.Headers)
	started := time.Now()
	resp, err := s.client.Do(up)
	if err != nil {
		if ctx.Err() == nil {
			log.Warn("model proxy: upstream unreachable", "provider", providerID, "err", errors.Unwrap(err))
		}
		proxyError(w, http.StatusBadGateway, "upstream_unreachable", "The model provider could not be reached")
		return
	}
	defer resp.Body.Close()
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
	scan := newUsageScanner(strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream"))
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			scan.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				break // the agent went away; still count what was used
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode >= 300 {
		log.Info("model proxy: provider refused", "agent", agentID, "model", m.Model.UpstreamID, "status", resp.StatusCode)
		return
	}
	u := scan.Finish()
	if u.Empty() {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.RecordUsage(rctx, teamID, agentID, m.Model.ID, "proxy", u); err != nil {
		log.Warn("model proxy: record usage", "agent", agentID, "err", err)
	}
	log.Debug("model proxy", "agent", agentID, "model", m.Model.UpstreamID, "input", u.Input, "output", u.Output,
		"ms", time.Since(started).Milliseconds())
}

// usageScanner reads token counts from a reply as it streams: the "usage"
// objects of server-sent events (OpenAI chat completions' last chunk,
// Responses' response.completed, Anthropic's message_start and
// message_delta), or of a whole JSON reply. Counts are merged by maximum, as
// streams repeat or accumulate them.
type usageScanner struct {
	sse  bool
	line []byte
	body bytes.Buffer
	u    Usage
}

const maxScannedBody = 16 << 20

func newUsageScanner(sse bool) *usageScanner { return &usageScanner{sse: sse} }

func (s *usageScanner) Write(p []byte) {
	if !s.sse {
		if s.body.Len()+len(p) <= maxScannedBody {
			s.body.Write(p)
		}
		return
	}
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if len(s.line)+len(p) <= maxScannedBody {
				s.line = append(s.line, p...)
			}
			return
		}
		s.line = append(s.line, p[:i]...)
		s.event(s.line)
		s.line = s.line[:0]
		p = p[i+1:]
	}
}

func (s *usageScanner) event(line []byte) {
	data, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r"), []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return
	}
	s.merge(data)
}

// Finish returns the counts (parsing a whole JSON reply).
func (s *usageScanner) Finish() Usage {
	if !s.sse && s.body.Len() > 0 {
		s.merge(s.body.Bytes())
	} else if s.sse && len(s.line) > 0 {
		s.event(s.line)
	}
	return s.u
}

type usageFields struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheRead           int64 `json:"cache_read_input_tokens"`
	CacheWrite          int64 `json:"cache_creation_input_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails *struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (s *usageScanner) merge(data []byte) {
	var obj struct {
		Usage    *usageFields `json:"usage"`
		Response *struct {
			Usage *usageFields `json:"usage"`
		} `json:"response"`
		Message *struct {
			Usage *usageFields `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(data, &obj) != nil {
		return
	}
	for _, u := range []*usageFields{obj.Usage, responseUsage(obj.Response), messageUsage(obj.Message)} {
		if u == nil {
			continue
		}
		in, out := max(u.PromptTokens, u.InputTokens), max(u.CompletionTokens, u.OutputTokens)
		cacheRead := u.CacheRead
		if u.PromptTokensDetails != nil {
			cacheRead = max(cacheRead, u.PromptTokensDetails.CachedTokens)
		}
		if u.InputTokensDetails != nil {
			cacheRead = max(cacheRead, u.InputTokensDetails.CachedTokens)
		}
		s.u.Input, s.u.Output = max(s.u.Input, in), max(s.u.Output, out)
		s.u.CacheRead, s.u.CacheWrite = max(s.u.CacheRead, cacheRead), max(s.u.CacheWrite, u.CacheWrite)
	}
}

func responseUsage(r *struct {
	Usage *usageFields `json:"usage"`
}) *usageFields {
	if r == nil {
		return nil
	}
	return r.Usage
}

func messageUsage(m *struct {
	Usage *usageFields `json:"usage"`
}) *usageFields {
	if m == nil {
		return nil
	}
	return m.Usage
}
