package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// meRoute is GET /v1/me: the signed-in user. With optional=true a request
// without a session cookie (or bearer credential) gets {"data": null}
// instead of a 401, so the web app's signed-out pages don't log an error on
// every load. An expired session is still a 401 (and clears the cookie). A
// personal API key or an app's token gets its person too (person).
func (a *api) meRoute() http.Handler {
	withPerson := a.person(a.getMe)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("optional") == "true" && r.Header.Get("Authorization") == "" && !a.Auth.HasSessionCookie(r) {
			httpx.JSON(w, http.StatusOK, nil)
			return
		}
		withPerson.ServeHTTP(w, r)
	})
}

func (a *api) getMe(w http.ResponseWriter, r *http.Request) {
	user, csrf, actor := dbgen.User{}, "", a.actor(r)
	if id, ok := auth.FromContext(r.Context()); ok {
		user, csrf = id.User, id.CSRFToken
	} else {
		// A bearer credential: its person, as of now. There is no CSRF token
		// (bearer requests need none).
		u, err := a.q.GetUser(r.Context(), actor.UserID)
		if failed(w, r, err) {
			return
		}
		user = u
	}
	myTeams, err := a.Teams.ListMine(r.Context(), user.ID)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	groups := user.Groups
	if groups == nil {
		groups = []string{}
	}
	// Capabilities are the caller's, not the person's: an app's token acts
	// without the person's platform role.
	httpx.JSON(w, http.StatusOK, apitypes.Me{
		Groups:    groups,
		User:      toAPIUser(user),
		CsrfToken: csrf,
		Capabilities: apitypes.Capabilities{
			PlatformAdmin:   actor.PlatformRole == authz.PlatformAdmin,
			PlatformAuditor: actor.PlatformRole == authz.PlatformAuditor,
		},
		Teams: toAPIMyTeams(myTeams),
	})
}
