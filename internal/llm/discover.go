package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
)

// newUpstreamClient calls providers: no overall timeout (replies stream for
// minutes), bounded connection set-up and time to the first byte, and no
// redirects (a provider URL is used exactly as configured).
func newUpstreamClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 10 * time.Minute
	t.MaxIdleConnsPerHost = 32
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// authenticate sets a provider's key and extra headers on a request.
func authenticate(req *http.Request, kind Kind, key string, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if kind.AuthHeader == "x-api-key" {
		req.Header.Set("x-api-key", key)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

// Discovered is a model a provider offers.
type Discovered struct {
	ID            string
	Name          string
	ContextWindow *int32
	// Added: the catalog already has it for this provider.
	Added bool
}

// Discover asks a provider which models it offers (platform admins).
func (s *Service) Discover(ctx context.Context, a authz.Actor, providerID uuid.UUID) ([]Discovered, error) {
	if err := requireAdmin(a); err != nil {
		return nil, err
	}
	p, err := s.q.GetModelProvider(ctx, providerID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return nil, errProviderNotFound
	} else if err != nil {
		return nil, err
	}
	kind, ok := KindByID(p.Kind)
	if !ok {
		return nil, errBadKind
	}
	key, err := s.box.Open(p.APIKeyEnc, providerAD(p.ID))
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if len(p.HeadersEnc) > 0 {
		raw, err := s.box.Open(p.HeadersEnc, providerAD(p.ID))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &headers); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target := p.BaseURL + kind.ModelsPath
	if kind.AuthHeader == "x-api-key" {
		target += "?limit=1000"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	authenticate(req, kind, string(key), headers)
	resp, err := s.client.Do(req)
	if err != nil {
		// The error names the URL, never the key.
		return nil, apperr.Conflict("discovery_failed", fmt.Sprintf("Could not reach the provider: %v", errors.Unwrap(err)))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.Conflict("discovery_failed", fmt.Sprintf("The provider answered %d to %s", resp.StatusCode, kind.ModelsPath))
	}
	var body struct {
		Data []struct {
			ID               string `json:"id"`
			Name             string `json:"name"`
			DisplayName      string `json:"display_name"`
			ContextLength    int64  `json:"context_length"`
			ContextWindow    int64  `json:"context_window"`
			MaxContextLength int64  `json:"max_context_length"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, apperr.Conflict("discovery_failed", "The provider's model list could not be read")
	}
	existing, err := s.q.ListModels(ctx, uuid.NullUUID{UUID: p.ID, Valid: true})
	if err != nil {
		return nil, err
	}
	added := map[string]bool{}
	for _, m := range existing {
		added[m.Model.UpstreamID] = true
	}
	out := make([]Discovered, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID == "" || len(d.ID) > 200 {
			continue
		}
		name := d.DisplayName
		if name == "" {
			name = d.Name
		}
		if name == "" || len([]rune(name)) > 100 {
			name = d.ID
		}
		item := Discovered{ID: d.ID, Name: name, Added: added[d.ID]}
		for _, c := range []int64{d.ContextWindow, d.ContextLength, d.MaxContextLength} {
			if c >= 1024 && c <= 100000000 {
				v := int32(c)
				item.ContextWindow = &v
				break
			}
		}
		out = append(out, item)
	}
	return out, nil
}
