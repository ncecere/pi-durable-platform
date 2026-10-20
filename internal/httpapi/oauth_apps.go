// OAuth Connect for apps with many people (api/openapi.yaml, tag oauth):
// the app-level endpoints a trusted app calls as itself with its secret (one
// event stream for everyone who connected it, and their usage), and the
// platform admin's side: app secrets and the apps people connected.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
)

func (a *api) oauthAppRoutes() []route {
	return []route{
		{"GET", "/v1/app/events", a.appClient(a.getAppEvents)},
		{"GET", "/v1/app/usage", a.appClient(a.getAppUsage)},
		{"GET", "/v1/admin/oauth-clients", a.admin(a.adminListConnectedApps)},
		{"GET", "/v1/admin/settings/oauth-clients/secrets", a.admin(a.adminListAppSecrets)},
		{"POST", "/v1/admin/settings/oauth-clients/secrets", a.admin(a.adminCreateAppSecret)},
		{"DELETE", "/v1/admin/settings/oauth-clients/secrets/{secretId}", a.admin(a.adminDeleteAppSecret)},
	}
}

// appClientKey carries the app an app secret authenticated.
type appClientKey struct{}

// appClient requires an app secret (Authorization: Bearer pdpcs_…) of a
// trusted app: never a session, a person's token or a key. Requests are
// limited per secret like any caller's (REQUESTS_PER_MINUTE).
func (a *api) appClient(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || a.OAuth == nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Use Authorization: Bearer <app secret> (a trusted app's secret)")
			return
		}
		c, err := a.OAuth.AuthenticateClient(r.Context(), strings.TrimSpace(secret))
		if errors.Is(err, oauth.ErrInvalidClient) {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "The app secret is invalid or deleted, or the app isn't trusted")
			return
		} else if err != nil {
			httpx.Internal(w, r, err)
			return
		}
		if !a.Auth.AdmitRequest(w, r, "oauth-app:"+c.SecretID.String()) {
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), appClientKey{}, c)))
	})
}

func appFrom(r *http.Request) oauth.AppClient {
	c, _ := r.Context().Value(appClientKey{}).(oauth.AppClient)
	return c
}

// getAppEvents is the app-level event stream: the thread changes everyone
// who connected the app may see, each with their subjects (agents.SubscribeAppThreads).
func (a *api) getAppEvents(w http.ResponseWriter, r *http.Request) {
	c := appFrom(r)
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "internal", "Streaming is not supported")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := a.Agents.SubscribeAppThreads(ctx, c.ClientID, a.OAuth.Resource(), func(ctx context.Context) bool {
		return a.OAuth.ClientStillValid(ctx, c)
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	if r.URL.Query().Get("once") == "true" {
		return
	}
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	closing := a.Agents.Host().Closing()
	for {
		select {
		case <-ctx.Done():
			return
		case <-closing:
			return
		case <-keepAlive.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				return
			}
			body := apitypes.AppThreadEvent{Subjects: ev.Subjects, AgentId: ev.AgentID, ThreadId: ev.Thread.ThreadID, Deleted: ev.Deleted}
			if !ev.Deleted {
				t := toAPIThread(ev.Thread)
				body.Thread = &t
			}
			b, _ := json.Marshal(body)
			_, _ = io.WriteString(w, "event: thread\ndata: "+string(b)+"\n\n")
			flusher.Flush()
		}
	}
}

// getAppUsage is a page of the usage of the app's people, by person and day.
func (a *api) getAppUsage(w http.ResponseWriter, r *http.Request) {
	q := oauth.AppUsageQuery{Cursor: r.URL.Query().Get("cursor")}
	for _, d := range []struct {
		name string
		dst  *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		if v := r.URL.Query().Get(d.name); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				failed(w, r, apperr.Invalid("invalid_range", d.name+" must be a date (YYYY-MM-DD)"))
				return
			}
			*d.dst = t
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			failed(w, r, apperr.Invalid("invalid_limit", "limit is 1 to 1000"))
			return
		}
		q.Limit = n
	}
	if len(q.Cursor) > 200 {
		failed(w, r, apperr.Invalid("invalid_cursor", "The cursor isn't one this endpoint returned"))
		return
	}
	page, err := a.OAuth.AppUsage(r.Context(), appFrom(r), q)
	if failed(w, r, err) {
		return
	}
	out := apitypes.AppUsagePage{From: openapi_types.Date{Time: page.From}, To: openapi_types.Date{Time: page.To},
		Rows: make([]apitypes.AppUsageRow, len(page.Rows))}
	for i, u := range page.Rows {
		out.Rows[i] = apitypes.AppUsageRow{Day: openapi_types.Date{Time: u.Day}, Subject: u.Subject, Requests: u.Requests,
			InputTokens: u.Input, OutputTokens: u.Output, CacheReadTokens: u.CacheRead, CacheWriteTokens: u.CacheWrite,
			CostUsd: u.CostUSD, UnpricedRequests: u.UnpricedRequests, ToolCalls: u.ToolCalls}
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *api) adminListConnectedApps(w http.ResponseWriter, r *http.Request) {
	apps, err := a.OAuth.ConnectedClients(r.Context(), a.actor(r))
	writeList(w, r, apps, err, func(c oauth.ConnectedClient) apitypes.ConnectedApp {
		return apitypes.ConnectedApp{ClientId: c.ClientID, Name: c.Name, Kind: apitypes.ConnectedAppKind(c.Kind), People: c.People,
			LastActiveAt: c.LastActiveAt, Trusted: c.Trusted}
	})
}

func toAPIAppSecret(s oauth.ClientSecret) apitypes.AppSecret {
	return apitypes.AppSecret{Id: s.ID, ClientId: s.ClientID, Prefix: s.Prefix, CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
		Trusted: s.Trusted}
}

func (a *api) adminListAppSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := a.OAuth.ListClientSecrets(r.Context(), a.actor(r))
	writeList(w, r, secrets, err, toAPIAppSecret)
}

func (a *api) adminCreateAppSecret(w http.ResponseWriter, r *http.Request) {
	var in apitypes.AppSecretCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	s, err := a.OAuth.CreateClientSecret(r.Context(), a.actor(r), strings.TrimSpace(in.ClientId))
	if failed(w, r, err) {
		return
	}
	v := toAPIAppSecret(s.ClientSecret)
	httpx.JSON(w, http.StatusCreated, apitypes.AppSecretCreated{Id: v.Id, ClientId: v.ClientId, Prefix: v.Prefix, CreatedAt: v.CreatedAt,
		LastUsedAt: v.LastUsedAt, Trusted: v.Trusted, Secret: s.Secret})
}

func (a *api) adminDeleteAppSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "secretId")
	if !ok {
		return
	}
	writeOK(w, r, a.OAuth.DeleteClientSecret(r.Context(), a.actor(r), id))
}
