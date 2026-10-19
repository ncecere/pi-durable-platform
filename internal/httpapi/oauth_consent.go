// OAuth Connect, the /v1 side (api/openapi.yaml, tag oauth): the consent
// page's view of an authorization request and its decision, a person's
// connected apps (theirs, or anyone's for platform admins and auditors), and
// the trusted-app list. All take browser sessions only: an app can't connect
// apps or approve itself.

package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// oauthRESTRoutes: the consent page, connected apps and trusted apps.
func (a *api) oauthRESTRoutes() []route {
	return []route{
		{"GET", "/v1/oauth/consent", a.session(a.getOAuthConsent)},
		{"POST", "/v1/oauth/consent", a.session(a.decideOAuthConsent)},
		{"GET", "/v1/oauth/client-logo", a.session(a.getOAuthClientLogo)},
		{"GET", "/v1/me/oauth-grants", a.session(a.listMyOAuthGrants)},
		{"DELETE", "/v1/me/oauth-grants/{grantId}", a.session(a.revokeMyOAuthGrant)},
		{"GET", "/v1/admin/users/{userId}/oauth-grants", a.admin(a.adminListUserOAuthGrants)},
		{"DELETE", "/v1/admin/users/{userId}/oauth-grants/{grantId}", a.admin(a.adminRevokeUserOAuthGrant)},
		{"GET", "/v1/admin/settings/oauth-clients", a.admin(a.adminGetTrustedClients)},
		{"PUT", "/v1/admin/settings/oauth-clients", a.admin(a.adminUpdateTrustedClients)},
	}
}

// consentRequest is a checked authorization request as the consent page
// sends it.
type consentRequest struct {
	client oauth.Client
	req    oauth.AuthorizeRequest
	user   dbgen.User
}

// checkConsent reads and checks an authorization request for the consent
// page. A request that isn't valid is a 400 with the reason (the page shows
// it and never sends the browser anywhere).
func (a *api) checkConsent(w http.ResponseWriter, r *http.Request, q url.Values) (consentRequest, bool) {
	id, _ := auth.FromContext(r.Context())
	cr := consentRequest{user: id.User}
	var err error
	if cr.req, err = oauth.RequestFromQuery(q); err == nil {
		cr.client, err = a.OAuth.Check(r.Context(), cr.req)
	}
	var oe *oauth.Error
	if errors.As(err, &oe) {
		httpx.Error(w, http.StatusBadRequest, "oauth_"+oe.Code, oe.Description)
		return cr, false
	}
	return cr, !failed(w, r, err)
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toAPIClientInfo(c oauth.Client, trusted bool) apitypes.OAuthClientInfo {
	return apitypes.OAuthClientInfo{Id: c.ID, Kind: apitypes.OAuthClientInfoKind(c.Kind), Name: c.Name, Host: c.Host(),
		Uri: nonEmpty(c.URI), LogoUrl: nonEmpty(c.LogoURI), Trusted: trusted && c.Kind == oauth.KindMetadata}
}

func (a *api) getOAuthConsent(w http.ResponseWriter, r *http.Request) {
	cr, ok := a.checkConsent(w, r, r.URL.Query())
	if !ok {
		return
	}
	trusted, err := a.OAuth.Trusted(r.Context(), cr.client)
	if failed(w, r, err) {
		return
	}
	remembered, err := a.OAuth.Remembered(r.Context(), cr.user.ID, cr.client.ID)
	if failed(w, r, err) {
		return
	}
	redirect, _ := url.Parse(cr.req.RedirectURI)
	out := apitypes.OAuthConsent{
		Client: toAPIClientInfo(cr.client, trusted), RedirectUri: cr.req.RedirectURI, RedirectHost: redirect.Host,
		Scopes: oauth.Scopes, Remembered: remembered,
	}
	// Where an app would make the person's own agents (they may also have
	// team agents; the page says so).
	if t, err := a.q.GetPersonalTeam(r.Context(), uuid.NullUUID{UUID: cr.user.ID, Valid: true}); err == nil {
		out.PersonalWorkspace = &apitypes.PersonalWorkspaceRef{Slug: t.Slug, Name: t.Name}
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		httpx.Internal(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *api) decideOAuthConsent(w http.ResponseWriter, r *http.Request) {
	var in apitypes.OAuthConsentDecision
	if !httpx.Decode(w, r, &in) {
		return
	}
	q, err := url.ParseQuery(in.Query)
	if err != nil || len(in.Query) > oauth.MaxQueryLen || !in.Decision.Valid() {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Send the authorization request's query and allow or deny.")
		return
	}
	cr, ok := a.checkConsent(w, r, q)
	if !ok {
		return
	}
	to := a.OAuth.DenyRedirect(cr.req)
	if in.Decision == apitypes.Allow {
		if to, err = a.OAuth.Approve(r.Context(), a.actor(r), cr.client, cr.req, oauth.ConsentAllowed); failed(w, r, err) {
			return
		}
	}
	httpx.JSON(w, http.StatusOK, apitypes.OAuthConsentResult{RedirectUrl: to})
}

// writeGrants writes a person's connected apps, marking trusted ones.
func (a *api) writeGrants(w http.ResponseWriter, r *http.Request, grants []oauth.Grant, err error) {
	if failed(w, r, err) {
		return
	}
	trusted, err := a.OAuth.TrustedIDs(r.Context())
	if failed(w, r, err) {
		return
	}
	writeList(w, r, grants, nil, func(g oauth.Grant) apitypes.OAuthGrant {
		c := oauth.GrantClient(g)
		return apitypes.OAuthGrant{Id: g.ID, Client: toAPIClientInfo(c, trusted[c.ID]), Scopes: g.Scopes, CreatedAt: g.CreatedAt,
			LastUsedAt: g.LastUsedAt}
	})
}

func (a *api) listMyOAuthGrants(w http.ResponseWriter, r *http.Request) {
	grants, err := a.OAuth.ListGrants(r.Context(), a.actor(r), a.actor(r).UserID)
	a.writeGrants(w, r, grants, err)
}

func (a *api) revokeMyOAuthGrant(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "grantId")
	if !ok {
		return
	}
	writeOK(w, r, a.OAuth.RevokeGrant(r.Context(), a.actor(r), a.actor(r).UserID, id))
}

func (a *api) adminListUserOAuthGrants(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathUUID(w, r, "userId")
	if !ok {
		return
	}
	grants, err := a.OAuth.ListGrants(r.Context(), a.actor(r), userID)
	a.writeGrants(w, r, grants, err)
}

func (a *api) adminRevokeUserOAuthGrant(w http.ResponseWriter, r *http.Request) {
	var userID, id uuid.UUID
	var ok bool
	if userID, ok = pathUUID(w, r, "userId"); !ok {
		return
	}
	if id, ok = pathUUID(w, r, "grantId"); !ok {
		return
	}
	writeOK(w, r, a.OAuth.RevokeGrant(r.Context(), a.actor(r), userID, id))
}

func toAPITrustedClients(t oauth.TrustedClients) apitypes.TrustedClients {
	out := apitypes.TrustedClients{Clients: make([]apitypes.TrustedClient, len(t.Clients)), Revision: t.Revision, UpdatedAt: t.UpdatedAt}
	for i, c := range t.Clients {
		out.Clients[i] = apitypes.TrustedClient{ClientId: c.ClientID, Label: nonEmpty(c.Label)}
	}
	return out
}

func (a *api) adminGetTrustedClients(w http.ResponseWriter, r *http.Request) {
	t, err := a.OAuth.GetTrustedClients(r.Context(), a.actor(r))
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, t.Revision, toAPITrustedClients(t))
}

func (a *api) adminUpdateTrustedClients(w http.ResponseWriter, r *http.Request) {
	in, rev, ok := decodeRevised[apitypes.TrustedClientsUpdate](w, r)
	if !ok {
		return
	}
	clients := make([]oauth.TrustedClient, len(in.Clients))
	for i, c := range in.Clients {
		clients[i] = oauth.TrustedClient{ClientID: c.ClientId}
		setIf(&clients[i].Label, c.Label)
	}
	t, err := a.OAuth.UpdateTrustedClients(r.Context(), a.actor(r), clients, rev)
	if failed(w, r, err) {
		return
	}
	writeRevised(w, http.StatusOK, t.Revision, toAPITrustedClients(t))
}

// logoRatePerMinute is how many logos a person may ask for a minute (a page
// shows a handful; browsers keep them for an hour).
const logoRatePerMinute = 120

// getOAuthClientLogo serves a known app's logo from the platform's own
// origin (the console's CSP allows no other image source). Any signed-in
// person may fetch the logo of any client the platform knows: a logo is
// public by nature. The address is the client's, never the request's.
func (a *api) getOAuthClientLogo(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	ok, wait, err := ratelimit.Allow(r.Context(), a.limiter, ratelimit.Bucket("oauth:logo", id.User.ID.String()), logoRatePerMinute)
	if failed(w, r, err) {
		return
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfter(wait)))
		httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again shortly.")
		return
	}
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "Give the app's client_id.")
		return
	}
	logo, err := a.OAuth.ClientLogo(r.Context(), clientID)
	if failed(w, r, err) {
		return
	}
	h := w.Header()
	h.Set("Content-Type", logo.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(logo.Data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(logo.Data)
}
