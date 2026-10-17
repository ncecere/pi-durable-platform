package httpapi

import (
	"net/http"

	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
)

// meRoute is GET /v1/me: the signed-in user. With optional=true a request
// without a session cookie gets {"data": null} instead of a 401, so the web
// app's signed-out pages don't log an error on every load. An expired
// session is still a 401 (and clears the cookie).
func (a *api) meRoute() http.Handler {
	withSession := a.session(a.getMe)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("optional") == "true" && r.Header.Get("Authorization") == "" && !a.Auth.HasSessionCookie(r) {
			httpx.JSON(w, http.StatusOK, nil)
			return
		}
		withSession.ServeHTTP(w, r)
	})
}

func (a *api) getMe(w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	myTeams, err := a.Teams.ListMine(r.Context(), id.User.ID)
	if err != nil {
		httpx.Internal(w, r, err)
		return
	}
	groups := id.User.Groups
	if groups == nil {
		groups = []string{}
	}
	httpx.JSON(w, http.StatusOK, apitypes.Me{
		Groups:    groups,
		User:      toAPIUser(id.User),
		CsrfToken: id.CSRFToken,
		Capabilities: apitypes.Capabilities{
			PlatformAdmin:   id.IsPlatformAdmin(),
			PlatformAuditor: id.IsPlatformAuditor(),
		},
		Teams: toAPIMyTeams(myTeams),
	})
}
