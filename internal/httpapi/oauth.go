// OAuth Connect (DESIGN.md "OAuth Connect"): the protocol endpoints of the
// authorization server in internal/oauth, through which an app acts as the
// person who connects it. They speak a protocol (OAuth 2.1, RFC 8414, RFC
// 9728, RFC 7591, RFC 7009), not the /v1 envelope, so they are outside
// api/openapi.yaml and the contract test skips them (protocolRoutes).

package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/store"
)

const (
	// maxOAuthBody bounds token, revocation and registration requests.
	maxOAuthBody = 16 << 10
	// Per-address rates (per minute) of the protocol endpoints.
	oauthRatePerMinute    = 60
	registerRatePerMinute = 10
	// connectConsentPath is the web app's consent page. It is not under
	// /oauth/, which never falls back to the app.
	connectConsentPath = "/connect/consent"
)

// protocolRoutes are served but not in api/openapi.yaml: OAuth Connect's
// protocol endpoints and the MCP identity token's keys (mcp_identity.go; the
// contract test skips them).
var protocolRoutes = map[string]bool{
	"GET " + mcpidentity.JWKSPath:                 true,
	"GET /.well-known/oauth-protected-resource":   true,
	"GET /.well-known/oauth-authorization-server": true,
	"GET /oauth/authorize":                        true,
	"POST /oauth/token":                           true,
	"POST /oauth/revoke":                          true,
	"POST /oauth/register":                        true,
	"GET /oauth/token":                            true,
	"GET /oauth/revoke":                           true,
	"GET /oauth/register":                         true,
}

// oauthRoutes serve the protocol endpoints.
func (a *api) oauthRoutes() []route {
	on := func(name string, perMinute int, h http.HandlerFunc) http.Handler {
		return a.oauthGate(name, perMinute, h)
	}
	return []route{
		{"GET", "/.well-known/oauth-protected-resource", http.HandlerFunc(a.oauthResourceMetadata)},
		{"GET", "/.well-known/oauth-authorization-server", http.HandlerFunc(a.oauthServerMetadata)},
		{"GET", "/oauth/authorize", on("authorize", oauthRatePerMinute, a.oauthAuthorize)},
		{"POST", "/oauth/token", on("token", oauthRatePerMinute, a.oauthToken)},
		{"POST", "/oauth/revoke", on("revoke", oauthRatePerMinute, a.oauthRevoke)},
		{"POST", "/oauth/register", on("register", registerRatePerMinute, a.oauthRegister)},
		// Without these, a GET would be a JSON 404 that doesn't say why.
		{"GET", "/oauth/token", http.HandlerFunc(oauthPostOnly)},
		{"GET", "/oauth/revoke", http.HandlerFunc(oauthPostOnly)},
		{"GET", "/oauth/register", http.HandlerFunc(oauthPostOnly)},
	}
}

// oauthPostOnly answers a GET on an endpoint that only takes POST (405).
func oauthPostOnly(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	oauthJSONError(w, http.StatusMethodNotAllowed, "invalid_request", "This endpoint only accepts POST.")
}

// oauthGate applies the endpoint's per-address rate.
func (a *api) oauthGate(name string, perMinute int, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIP(r, a.Config.TrustedProxies)
		ok, wait, err := ratelimit.Allow(r.Context(), a.limiter, ratelimit.Bucket("oauth:"+name, ip), perMinute)
		if err != nil {
			a.oauthFail(w, r, err)
			return
		}
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfter(wait)))
			oauthJSONError(w, http.StatusTooManyRequests, "slow_down", "Too many requests. Try again shortly.")
			return
		}
		h(w, r)
	})
}

// oauthJSON writes a protocol response: plain JSON, never cached.
func oauthJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthJSONError writes an OAuth error response (RFC 6749 §5.2).
func oauthJSONError(w http.ResponseWriter, status int, code, description string) {
	oauthJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// oauthFail writes err: an OAuth error as itself, anything else as a
// logged server_error.
func (a *api) oauthFail(w http.ResponseWriter, r *http.Request, err error) {
	var oe *oauth.Error
	if errors.As(err, &oe) {
		oauthJSONError(w, oe.Status, oe.Code, oe.Description)
		return
	}
	a.Log.ErrorContext(r.Context(), "oauth", "err", err, "request_id", httpx.RequestID(r.Context()))
	oauthJSONError(w, http.StatusInternalServerError, "server_error", "Something went wrong. Try again shortly.")
}

// publicJSON writes cacheable protocol metadata.
func publicJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_ = json.NewEncoder(w).Encode(v)
}

// oauthResourceMetadata is the platform's protected resource metadata (RFC
// 9728), at the address a refused app token's 401 names.
func (a *api) oauthResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	publicJSON(w, map[string]any{
		"resource":                 a.OAuth.Resource(),
		"authorization_servers":    []string{a.OAuth.Issuer()},
		"scopes_supported":         oauth.Scopes,
		"bearer_methods_supported": []string{"header"},
		"resource_name":            a.Config.Instance.Name,
	})
}

// oauthServerMetadata is the authorization server metadata (RFC 8414).
func (a *api) oauthServerMetadata(w http.ResponseWriter, _ *http.Request) {
	iss := a.OAuth.Issuer()
	publicJSON(w, map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + "/oauth/authorize",
		"token_endpoint":                                 iss + "/oauth/token",
		"registration_endpoint":                          iss + "/oauth/register",
		"revocation_endpoint":                            iss + "/oauth/revoke",
		"scopes_supported":                               oauth.Scopes,
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"revocation_endpoint_auth_methods_supported":     []string{"none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"prompt_values_supported":                        []string{oauth.PromptNone, oauth.PromptConsent},
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

// oauthAuthorize is the authorization endpoint. A request whose client or
// redirect URI isn't good gets a page (never a redirect); other errors go
// back to the client. Without a browser session it sends the person to sign
// in (the platform's own sign-in, so OIDC_ALLOWED_GROUPS and the rest apply;
// login_hint goes to the identity provider) and back here. Signed in as the
// hinted person (or with no hint), a client they already allowed for the
// scopes, or a trusted client, goes straight back with a code, and anything
// else goes to the consent page of the web app with the same request (which
// also explains a login_hint that isn't the signed-in person and offers to
// switch accounts). With prompt=none no page is ever shown: what would need
// one goes back to the app as login_required, consent_required or
// interaction_required (internal/oauth/prompt.go); the hint is compared only
// for an app the person connected or a trusted one, so an app learns nothing
// about which account is signed in from it.
func (a *api) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.RawQuery) > oauth.MaxQueryLen {
		a.oauthPage(w, r, oauth.PageError("invalid_request", "The request is too long."))
		return
	}
	req, err := oauth.RequestFromQuery(r.URL.Query())
	if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	c, err := a.OAuth.Check(r.Context(), req)
	var oe *oauth.Error
	if errors.As(err, &oe) && !oe.ShowPage() {
		http.Redirect(w, r, a.OAuth.ErrorRedirect(req, oe), http.StatusFound)
		return
	} else if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	silent := req.PromptNone()
	needs := func(code string) {
		http.Redirect(w, r, a.OAuth.ErrorRedirect(req, oauth.PromptError(code)), http.StatusFound)
	}
	user, ok, err := a.Auth.SessionUser(r)
	if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	switch {
	case !ok && silent:
		needs("login_required")
		return
	case !ok:
		http.Redirect(w, r, a.Auth.LoginURLWithHint("/oauth/authorize?"+r.URL.RawQuery, req.LoginHint), http.StatusFound)
		return
	case !store.IsUserActive(user) && silent:
		needs("interaction_required")
		return
	case !store.IsUserActive(user):
		a.oauthPage(w, r, oauth.PageError("access_denied", "This account is suspended or no longer in a group allowed to use this platform."))
		return
	}
	trusted, err := a.OAuth.Trusted(r.Context(), c)
	if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	if silent && !trusted && !oauth.HintMatches(user, req.LoginHint) {
		// The hint is compared only for an app this person connected (for any
		// scope): otherwise login_required against consent_required would tell
		// any app whether the browser is signed in as a given email.
		connected, err := a.OAuth.Remembered(r.Context(), user.ID, c.ID, nil)
		if err != nil {
			a.oauthPage(w, r, err)
			return
		}
		if !connected {
			needs("consent_required")
			return
		}
	}
	switch {
	case !oauth.HintMatches(user, req.LoginHint) && silent:
		// Never the wrong account: the hinted person has to sign in.
		needs("login_required")
		return
	case !oauth.HintMatches(user, req.LoginHint):
		// The consent page says whom the app expected and offers to switch.
		http.Redirect(w, r, connectConsentPath+"?"+r.URL.RawQuery, http.StatusFound)
		return
	}
	remembered, err := a.OAuth.Remembered(r.Context(), user.ID, c.ID, req.Scopes())
	if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	if !trusted && !remembered && silent {
		needs("consent_required")
		return
	}
	if req.PromptConsent() || (!trusted && !remembered) {
		http.Redirect(w, r, connectConsentPath+"?"+r.URL.RawQuery, http.StatusFound)
		return
	}
	actor := auth.Identity{User: user}.Actor(httpx.RequestID(r.Context()), httpx.ClientIP(r, a.Config.TrustedProxies))
	how := oauth.ConsentTrusted
	if !trusted {
		how = oauth.ConsentAllowed // remembered: no new consent is recorded
	}
	to, err := a.OAuth.Approve(r.Context(), actor, c, req, how)
	if err != nil {
		a.oauthPage(w, r, err)
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// oauthPageTmpl is the authorization error page. It follows the person's
// color mode like the web app: /color-mode.js (the app's own pre-paint
// script, same origin) sets <html data-theme> from their choice in this
// browser, else the system's; without it, the system's setting applies.
var oauthPageTmpl = template.Must(template.New("oauth").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<script src="/color-mode.js"></script>
<title>Can't connect this app · {{.Instance}}</title>
<style>:root{color-scheme:light;--bg:#fff;--fg:#1f2328;--muted:#59636e;--code:#eff1f3}
:root[data-theme=dark]{color-scheme:dark;--bg:#0d1117;--fg:#e6edf3;--muted:#9198a1;--code:#262c36}
@media (prefers-color-scheme:dark){:root:not([data-theme]){color-scheme:dark;--bg:#0d1117;--fg:#e6edf3;--muted:#9198a1;--code:#262c36}}
html{background:var(--bg)}body{font-family:system-ui,sans-serif;max-width:36rem;margin:4rem auto;padding:0 1rem;line-height:1.5;color:var(--fg);background:var(--bg)}
h1{font-size:1.4rem}small{color:var(--muted)}code{background:var(--code);padding:0 .25rem}</style></head>
<body><main><h1>Can't connect this app</h1><p>{{.Message}}</p>
<p>Nothing was shared with the app. Close this page and try connecting again, or ask the app's maker.</p>
<p><small>Error: <code>{{.Code}}</code></small></p></main></body></html>`))

// oauthPage shows an authorization error to the person (400; 500 for a
// failure of ours, logged).
func (a *api) oauthPage(w http.ResponseWriter, r *http.Request, err error) {
	status, code, msg := http.StatusBadRequest, "invalid_request", "The app's request isn't valid."
	var oe *oauth.Error
	if errors.As(err, &oe) {
		code, msg = oe.Code, oe.Description
	} else {
		a.Log.ErrorContext(r.Context(), "oauth authorize", "err", err, "request_id", httpx.RequestID(r.Context()))
		status, code, msg = http.StatusInternalServerError, "server_error", "Something went wrong. Try again shortly."
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_ = oauthPageTmpl.Execute(w, map[string]string{"Instance": a.Config.Instance.Name, "Message": msg, "Code": code})
}

// oauthForm reads a form-encoded protocol request (bounded).
func oauthForm(w http.ResponseWriter, r *http.Request) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/x-www-form-urlencoded" {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request", "Send the request as application/x-www-form-urlencoded.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if err := r.ParseForm(); err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request", "The request body couldn't be read.")
		return false
	}
	return true
}

// formClientID is the client_id in the form, or the user of a Basic
// Authorization header (clients that send it there with an empty secret).
func formClientID(r *http.Request) string {
	if id := r.PostForm.Get("client_id"); id != "" {
		return id
	}
	if user, _, ok := r.BasicAuth(); ok {
		return user
	}
	return ""
}

// oauthToken is the token endpoint.
func (a *api) oauthToken(w http.ResponseWriter, r *http.Request) {
	if !oauthForm(w, r) {
		return
	}
	f := r.PostForm
	out, err := a.OAuth.Token(r.Context(), oauth.TokenRequest{
		GrantType: f.Get("grant_type"), ClientID: formClientID(r), Code: f.Get("code"), RedirectURI: f.Get("redirect_uri"),
		CodeVerifier: f.Get("code_verifier"), RefreshToken: f.Get("refresh_token"), Resource: f.Get("resource"),
		Scope:     f.Get("scope"),
		RequestID: httpx.RequestID(r.Context()), ClientIP: httpx.ClientIP(r, a.Config.TrustedProxies),
	})
	if err != nil {
		a.oauthFail(w, r, err)
		return
	}
	oauthJSON(w, http.StatusOK, out)
}

// oauthRevoke is the revocation endpoint (RFC 7009): 200 whatever the token.
func (a *api) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	if !oauthForm(w, r) {
		return
	}
	token := r.PostForm.Get("token")
	if token == "" {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request", "Send the token to revoke.")
		return
	}
	err := a.OAuth.Revoke(r.Context(), token, formClientID(r), httpx.RequestID(r.Context()), httpx.ClientIP(r, a.Config.TrustedProxies))
	if err != nil {
		a.oauthFail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// oauthRegister is Dynamic Client Registration (RFC 7591) for apps that
// can't publish a metadata document: public clients only. application_type
// (web or native) is accepted and echoed.
func (a *api) oauthRegister(w http.ResponseWriter, r *http.Request) {
	var in oauth.ClientMetadata
	r.Body = http.MaxBytesReader(w, r.Body, maxOAuthBody)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "Send the client's metadata as a JSON object of at most 16 KiB.")
		return
	}
	reg, err := a.OAuth.Register(r.Context(), in, httpx.RequestID(r.Context()), httpx.ClientIP(r, a.Config.TrustedProxies))
	if err != nil {
		a.oauthFail(w, r, err)
		return
	}
	out := map[string]any{
		"client_id": reg.ID, "client_id_issued_at": reg.IssuedAt, "client_name": reg.Name, "redirect_uris": reg.RedirectURIs,
		"token_endpoint_auth_method": "none", "grant_types": []string{"authorization_code", "refresh_token"},
		"response_types": []string{"code"}, "scope": strings.Join(oauth.Scopes, " "),
	}
	if reg.ApplicationType != "" {
		out["application_type"] = reg.ApplicationType
	}
	if reg.URI != "" {
		out["client_uri"] = reg.URI
	}
	if reg.LogoURI != "" {
		out["logo_uri"] = reg.LogoURI
	}
	oauthJSON(w, http.StatusCreated, out)
}
