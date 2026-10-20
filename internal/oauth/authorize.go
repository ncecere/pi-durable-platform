package oauth

import (
	"context"
	"errors"
	"net/url"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// AuthorizeRequest is an authorization request (RFC 6749 §4.1.1 with PKCE,
// RFC 7636, and an optional resource indicator, RFC 8707).
type AuthorizeRequest struct {
	ClientID            string
	RedirectURI         string
	ResponseType        string
	CodeChallenge       string
	CodeChallengeMethod string
	Resource            string
	State               string
	Scope               string
	// Prompt is none and/or consent (prompt.go).
	Prompt string
	// LoginHint names the account the app expects (an email address).
	LoginHint string
}

// PromptNone reports whether the request asks for no page (prompt=none).
func (r AuthorizeRequest) PromptNone() bool {
	none, _, _ := checkPrompt(r.Prompt)
	return none
}

// PromptConsent reports whether the request asks for the consent page even
// when the person allowed the app before (prompt=consent).
func (r AuthorizeRequest) PromptConsent() bool {
	_, consent, _ := checkPrompt(r.Prompt)
	return consent
}

// Scopes are the request's scopes, normalized (nil when invalid).
func (r AuthorizeRequest) Scopes() []string {
	s, _ := parseScope(r.Scope)
	return s
}

// Bounds.
const (
	// maxStateLen bounds state (echoed back to the client).
	maxStateLen = 1000
	// MaxQueryLen bounds an authorization request's query string.
	MaxQueryLen = 8000
)

// pkceChallenge is an S256 challenge: base64url of a SHA-256, unpadded.
var pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// RequestFromQuery reads an authorization request from a query string. A
// parameter given twice is refused (RFC 6749 §3.1), before anything is
// known about the client, so as a page.
func RequestFromQuery(q url.Values) (AuthorizeRequest, error) {
	for k, v := range q {
		if len(v) > 1 {
			return AuthorizeRequest{}, pageError("invalid_request", "The request repeats the "+k+" parameter.")
		}
	}
	return AuthorizeRequest{
		ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), ResponseType: q.Get("response_type"),
		CodeChallenge: q.Get("code_challenge"), CodeChallengeMethod: q.Get("code_challenge_method"),
		Resource: q.Get("resource"), State: q.Get("state"), Scope: q.Get("scope"),
		Prompt: q.Get("prompt"), LoginHint: q.Get("login_hint"),
	}, nil
}

// Check validates a request. The client and its redirect URI come first:
// until both are known to be good an error is shown as a page (ShowPage),
// never sent anywhere, so the endpoint can't be an open redirect. Other
// errors go back to the client's redirect URI. A resource, when sent, must be
// the platform (APP_URL) exactly.
func (s *Service) Check(ctx context.Context, r AuthorizeRequest) (Client, error) {
	c, err := s.ResolveClient(ctx, r.ClientID)
	if err != nil {
		return Client{}, err
	}
	if r.RedirectURI == "" {
		return Client{}, pageError("invalid_request", "The request has no redirect_uri.")
	}
	if !matchRedirect(c.RedirectURIs, r.RedirectURI) {
		return Client{}, pageError("invalid_request", "The redirect_uri isn't one the client registered.")
	}
	if len(r.State) > maxStateLen {
		return Client{}, pageError("invalid_request", "The state parameter is too long.")
	}
	_, scopeOK := parseScope(r.Scope)
	_, _, promptErr := checkPrompt(r.Prompt)
	switch {
	case r.ResponseType != "code":
		return c, oauthError("unsupported_response_type", "Only response_type=code is supported.")
	case r.CodeChallenge == "":
		return c, oauthError("invalid_request", "PKCE is required: send code_challenge with code_challenge_method=S256.")
	case r.CodeChallengeMethod != "S256":
		return c, oauthError("invalid_request", "Only the S256 code_challenge_method is supported.")
	case !pkceChallenge.MatchString(r.CodeChallenge):
		return c, oauthError("invalid_request", "The code_challenge isn't an S256 challenge.")
	case r.Resource != "" && r.Resource != s.Resource():
		return c, oauthError("invalid_target", "The resource must be this platform's URL, "+s.Resource()+", or left out.")
	case !scopeOK:
		return c, oauthError("invalid_scope", errScopeText)
	case promptErr != nil:
		return c, promptErr
	case !validLoginHint(r.LoginHint):
		return c, oauthError("invalid_request", "The login_hint is too long or has control characters.")
	}
	return c, nil
}

// redirect is the redirect URI with params, the state and the issuer (RFC
// 9207). Call it only for a request that passed the page checks.
func (s *Service) redirect(r AuthorizeRequest, params url.Values) string {
	u, err := url.Parse(r.RedirectURI)
	if err != nil {
		return ""
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if r.State != "" {
		q.Set("state", r.State)
	}
	q.Set("iss", s.issuer)
	u.RawQuery = q.Encode()
	return u.String()
}

// ErrorRedirect is where to send an error that isn't shown as a page.
func (s *Service) ErrorRedirect(r AuthorizeRequest, e *Error) string {
	return s.redirect(r, url.Values{"error": {e.Code}, "error_description": {e.Description}})
}

// DenyRedirect is where to send the person who said no.
func (s *Service) DenyRedirect(r AuthorizeRequest) string {
	return s.ErrorRedirect(r, oauthError("access_denied", "The person declined."))
}

// Remembered reports whether the person has already allowed the client
// for the requested scopes (an active grant that covers them), so the
// consent page can be skipped.
func (s *Service) Remembered(ctx context.Context, userID uuid.UUID, clientID string, scopes []string) (bool, error) {
	g, err := s.q.GetActiveOAuthGrant(ctx, dbgen.GetActiveOAuthGrantParams{UserID: userID, ClientID: clientID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return false, nil
	}
	return err == nil && covers(g.Scopes, scopes), err
}

// How consent was given (audit metadata of oauth.consent).
const (
	ConsentAllowed = "allowed" // the person allowed it on the consent page
	ConsentTrusted = "trusted" // a trusted client: no consent screen
)

// Approve records the person's consent for a checked request's client (a
// grant, audited as oauth.consent the first time, and again when it widens
// to more scopes) and returns the redirect with a new authorization code:
// single use, CodeTTL, bound to the grant, client, redirect URI, PKCE
// challenge, resource and the scopes asked for.
func (s *Service) Approve(ctx context.Context, a authz.Actor, c Client, r AuthorizeRequest, how string) (string, error) {
	scopes, ok := parseScope(r.Scope)
	if !ok {
		return "", oauthError("invalid_scope", errScopeText)
	}
	code := newToken(codePrefix)
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		gr, err := s.grantFor(ctx, q, a, c, scopes, how)
		if err != nil {
			return err
		}
		if _, err := q.DeleteExpiredOAuthCodes(ctx); err != nil {
			return err
		}
		if c.Kind == KindRegistered {
			if err := q.TouchOAuthClient(ctx, c.ID); err != nil {
				return err
			}
		}
		return q.InsertOAuthCode(ctx, dbgen.InsertOAuthCodeParams{
			CodeHash: s.digest(code), GrantID: gr.ID, ClientID: c.ID, RedirectUri: r.RedirectURI,
			CodeChallenge: r.CodeChallenge, Resource: gr.Resource, ExpiresAt: s.now().Add(CodeTTL),
			Scopes: narrowest(gr.Scopes, scopes),
		})
	})
	if err != nil {
		return "", err
	}
	return s.redirect(r, url.Values{"code": {code}}), nil
}

// grantFor returns the person's active grant for the client, creating it
// (and auditing the consent) when there is none, and widening it (audited
// again) when it doesn't cover the scopes asked for. The person's subject
// for the client (their app-stable ID) is made on the way.
func (s *Service) grantFor(ctx context.Context, q *dbgen.Queries, a authz.Actor, c Client, scopes []string, how string) (dbgen.OauthGrant, error) {
	if _, err := q.EnsureOAuthClientSubject(ctx, dbgen.EnsureOAuthClientSubjectParams{ClientID: c.ID, UserID: a.UserID}); err != nil {
		return dbgen.OauthGrant{}, err
	}
	gr, err := q.GetActiveOAuthGrant(ctx, dbgen.GetActiveOAuthGrantParams{UserID: a.UserID, ClientID: c.ID})
	if err == nil {
		if covers(gr.Scopes, scopes) {
			return gr, nil
		}
		before := grantSnapshot(gr)
		if gr, err = q.UpdateOAuthGrantScopes(ctx, dbgen.UpdateOAuthGrantScopesParams{ID: gr.ID, Scopes: []string{ScopeAgents}}); err != nil {
			return gr, err
		}
		e := a.Audit("oauth.consent", "oauth_grant", gr.ID.String())
		e.Before, e.After = before, grantSnapshot(gr)
		e.Metadata = map[string]any{"how": how, "name": gr.ClientName, "scopes": gr.Scopes}
		return gr, audit.Record(ctx, q, e)
	}
	if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return gr, err
	}
	gr, err = q.InsertOAuthGrant(ctx, dbgen.InsertOAuthGrantParams{
		UserID: a.UserID, ClientID: c.ID, ClientKind: c.Kind, ClientName: c.Name, ClientUri: ptr(c.URI),
		LogoUri: ptr(c.LogoURI), Scopes: scopes, Resource: s.Resource(),
	})
	if apperr.IsUniqueViolation(err, "oauth_grants_active_key") {
		// Allowed twice at once: the other request's grant serves.
		return gr, apperr.Conflict("consent_conflict", "This app was just connected. Try again.")
	} else if err != nil {
		return gr, err
	}
	e := a.Audit("oauth.consent", "oauth_grant", gr.ID.String())
	e.After = grantSnapshot(gr)
	e.Metadata = map[string]any{"how": how, "name": gr.ClientName, "scopes": gr.Scopes}
	return gr, audit.Record(ctx, q, e)
}

func grantSnapshot(gr dbgen.OauthGrant) map[string]any {
	return map[string]any{"clientId": gr.ClientID, "clientKind": gr.ClientKind, "clientName": gr.ClientName, "clientUri": gr.ClientUri,
		"scopes": gr.Scopes, "resource": gr.Resource, "userId": gr.UserID}
}
