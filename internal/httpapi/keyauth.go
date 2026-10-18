package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
)

// keyActor carries an actor authenticated by a key or an app's token
// (bearer).
type keyActor struct{}

// sessionOrKey accepts a browser session, or a credential in the
// Authorization header: a personal API key (acts as its person), a team
// service account key (acts as the account, in its team, with its role), or
// an access token of an app the person connected (OAuth Connect: acts as the
// person, without their platform role). Bearer credentials need no CSRF
// token: browsers never send them on their own. Only agent routes (and what
// an app needs around them) take them.
func (a *api) sessionOrKey(h http.HandlerFunc) http.Handler { return a.withBearer(h, false) }

// person is sessionOrKey for routes about a person (who they are, their
// teams): a session, a personal API key or an app's token, never a service
// account key (403 person_required), which acts for no one.
func (a *api) person(h http.HandlerFunc) http.Handler { return a.withBearer(h, true) }

func (a *api) withBearer(h http.HandlerFunc, personOnly bool) http.Handler {
	session := a.session(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			session.ServeHTTP(w, r)
			return
		}
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Use Authorization: Bearer <key>")
			return
		}
		token = strings.TrimSpace(token)
		actor, limitKey, err := a.authenticateKey(r.Context(), token)
		if errors.Is(err, apikeys.ErrInvalid) || errors.Is(err, serviceaccounts.ErrInvalid) || errors.Is(err, oauth.ErrInvalidToken) {
			if oauth.LooksLikeToken(token) {
				// Tell the app where to get a new token (RFC 6750 §3, RFC 9728).
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+a.resourceMetadataURL()+`", error="invalid_token"`)
				httpx.Error(w, http.StatusUnauthorized, "unauthorized", "The access token is invalid, expired or revoked")
				return
			}
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "The key is invalid, expired or revoked")
			return
		} else if err != nil {
			httpx.Internal(w, r, err)
			return
		}
		if personOnly && actor.Service != nil {
			httpx.Error(w, http.StatusForbidden, "person_required",
				"A service account acts for no person: use a personal API key, an app's token or a browser session")
			return
		}
		if !a.Auth.AdmitRequest(w, r, limitKey) {
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), keyActor{}, actor)))
	})
}

func (a *api) authenticateKey(ctx context.Context, token string) (authz.Actor, string, error) {
	switch {
	case oauth.IsAccessToken(token):
		if a.OAuth == nil {
			return authz.Actor{}, "", oauth.ErrInvalidToken
		}
		p, err := a.OAuth.Authenticate(ctx, token)
		if err != nil {
			return authz.Actor{}, "", err
		}
		if err := a.OAuth.Touch(ctx, p.Grant.ID); err != nil {
			a.Log.WarnContext(ctx, "could not record a connected app's use", "err", err)
		}
		// The person, without their platform role: an app acting for a
		// platform admin gets no admin powers.
		return oauth.Actor(p.Grant, "", ""), "user:" + p.User.ID.String(), nil
	case serviceaccounts.LooksLikeKey(token):
		p, err := a.ServiceAccounts.Authenticate(ctx, token)
		if err != nil {
			return authz.Actor{}, "", err
		}
		_ = a.ServiceAccounts.Touch(ctx, p)
		return authz.Actor{PlatformRole: authz.PlatformNone, Service: &authz.ServiceActor{
			AccountID: p.Account.ID, Name: p.Account.Name, TeamID: p.Team.ID, Role: p.Account.Role, KeyName: p.Key.Name,
		}}, "service-account:" + p.Account.ID.String(), nil
	case apikeys.LooksLikeKey(token):
		p, err := a.APIKeys.Authenticate(ctx, token)
		if err != nil {
			return authz.Actor{}, "", err
		}
		_ = a.APIKeys.Touch(ctx, p.Key.ID)
		return authz.Actor{UserID: p.User.ID, PlatformRole: p.User.PlatformRole, APIKeyID: p.Key.ID, APIKeyName: p.Key.Name},
			"user:" + p.User.ID.String(), nil
	}
	if oauth.LooksLikeToken(token) {
		return authz.Actor{}, "", oauth.ErrInvalidToken
	}
	return authz.Actor{}, "", apikeys.ErrInvalid
}

// resourceMetadataURL is where the platform's protected resource metadata
// (RFC 9728) is served.
func (a *api) resourceMetadataURL() string {
	return strings.TrimRight(a.Config.AppURL, "/") + "/.well-known/oauth-protected-resource"
}
