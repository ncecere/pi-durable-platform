package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
)

// keyActor carries an actor authenticated by a key (keyAuth).
type keyActor struct{}

// sessionOrKey accepts a browser session, or a key in the Authorization
// header: a personal API key (acts as its person) or a team service account
// key (acts as the account, in its team, with its role). Keys need no CSRF
// token: browsers never send them on their own. Only agent routes take keys.
func (a *api) sessionOrKey(h http.HandlerFunc) http.Handler {
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
		actor, limitKey, err := a.authenticateKey(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, apikeys.ErrInvalid) || errors.Is(err, serviceaccounts.ErrInvalid) {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "The key is invalid, expired or revoked")
			return
		} else if err != nil {
			httpx.Internal(w, r, err)
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
	return authz.Actor{}, "", apikeys.ErrInvalid
}
