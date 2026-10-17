// Package httpapi assembles the HTTP surface: the route table, middleware,
// operational endpoints and the /v1 handlers.
//
// Every route is declared in a table (apiRoutes, grouped by area) so tests
// can verify that the served surface matches api/openapi.yaml exactly.
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/platform"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Deps are the dependencies shared by handlers. A new domain (for example
// agents) adds its service here, builds it in app.NewDeps, and adds its
// route groups to apiRoutes.
type Deps struct {
	Config   config.Config
	Pool     *pgxpool.Pool
	Auth     *auth.Service
	Teams    *teams.Service
	Platform *platform.Service
	// APIKeys manages personal API keys.
	APIKeys *apikeys.Service
	// ServiceAccounts manages team service accounts and their keys.
	ServiceAccounts *serviceaccounts.Service
	// Agents manages agents and relays conversations to their runtimes.
	Agents *agents.Service
	// Secrets seals values stored in the database with ENCRYPTION_KEY
	// (nil in tests that don't need it).
	Secrets *secrets.Box
	// RateStore counts sign-in attempts and per-person request limits (nil:
	// in memory, for tests).
	RateStore ratelimit.Store

	// Web is the built single-page app; WebBuilt is false when only the
	// placeholder is embedded.
	Web      fs.FS
	WebBuilt bool
	Health   *Health
	Log      *slog.Logger
}

type api struct {
	Deps
	q *dbgen.Queries
}

// route is one entry in the served surface.
type route struct {
	Method  string
	Path    string
	Handler http.Handler
}

func (r route) pattern() string { return r.Method + " " + r.Path }

func opsRoutes(d Deps) []route {
	return []route{
		{"GET", "/healthz", http.HandlerFunc(d.Health.Live)},
		{"GET", "/readyz", http.HandlerFunc(d.Health.Ready)},
	}
}

// apiRoutes is the full served surface, grouped by area.
func apiRoutes(d Deps) []route {
	a := &api{Deps: d}
	if d.Pool != nil {
		a.q = dbgen.New(d.Pool)
	}
	routes := opsRoutes(d)
	// Add a new area's route groups here (and its paths to api/openapi.yaml).
	for _, group := range [][]route{a.authRoutes(), a.teamRoutes(), a.platformAdminRoutes(), a.apiKeyRoutes(),
		a.serviceAccountRoutes(), a.serviceAccountAdminRoutes(), a.agentRoutes()} {
		routes = append(routes, group...)
	}
	return routes
}

// session requires a browser session.
func (a *api) session(h http.HandlerFunc) http.Handler { return a.Auth.RequireSession(h) }

// admin requires a session, then a platform admin or auditor. Write
// operations additionally require platform_admin inside the service.
func (a *api) admin(h http.HandlerFunc) http.Handler { return a.session(a.requirePlatformRead(h)) }

// authRoutes: sign-in and the caller's own identity.
func (a *api) authRoutes() []route {
	return []route{
		{"GET", "/auth/login", http.HandlerFunc(a.Auth.Login)},
		{"GET", "/auth/callback", http.HandlerFunc(a.Auth.Callback)},
		{"POST", "/auth/dev", http.HandlerFunc(a.Auth.DevLogin)},
		{"POST", "/auth/logout", a.session(a.Auth.Logout)},
		{"GET", "/v1/auth/config", http.HandlerFunc(a.Auth.AuthConfig)},
		{"GET", "/v1/me", a.meRoute()},
	}
}

// teamRoutes: teams, members, invites and the team audit log.
func (a *api) teamRoutes() []route {
	return []route{
		{"GET", "/v1/teams", a.session(a.listMyTeams)},
		{"GET", "/v1/teams/{team}", a.session(a.getTeam)},
		{"GET", "/v1/teams/{team}/members", a.session(a.listMembers)},
		{"POST", "/v1/teams/{team}/members", a.session(a.addMember)},
		{"PATCH", "/v1/teams/{team}/members/{userId}", a.session(a.updateMember)},
		{"DELETE", "/v1/teams/{team}/members/{userId}", a.session(a.removeMember)},
		{"GET", "/v1/teams/{team}/invites", a.session(a.listInvites)},
		{"DELETE", "/v1/teams/{team}/invites/{inviteId}", a.session(a.revokeInvite)},
		{"GET", "/v1/teams/{team}/audit", a.session(a.listTeamAudit)},
		{"GET", "/v1/teams/{team}/audit/{entryId}", a.session(a.getTeamAuditEntry)},
	}
}

// platformAdminRoutes: users, teams and the audit log across the platform.
func (a *api) platformAdminRoutes() []route {
	return []route{
		{"GET", "/v1/admin/users", a.admin(a.adminListUsers)},
		{"GET", "/v1/admin/users/{userId}", a.admin(a.adminGetUser)},
		{"PATCH", "/v1/admin/users/{userId}", a.admin(a.adminUpdateUser)},
		{"GET", "/v1/admin/teams", a.admin(a.adminListTeams)},
		{"POST", "/v1/admin/teams", a.admin(a.adminCreateTeam)},
		{"GET", "/v1/admin/teams/{team}", a.admin(a.adminGetTeam)},
		{"PATCH", "/v1/admin/teams/{team}", a.admin(a.adminUpdateTeam)},
		{"POST", "/v1/admin/teams/{team}/owners", a.admin(a.adminAssignOwner)},
		{"GET", "/v1/admin/audit", a.admin(a.adminListAudit)},
		{"GET", "/v1/admin/audit/{entryId}", a.admin(a.adminGetAuditEntry)},
	}
}

func build(d Deps, routes []route, fallback http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes {
		mux.Handle(rt.pattern(), rt.Handler)
	}
	mux.Handle("/", fallback)
	return chain(mux, d)
}

var notFound = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, http.StatusNotFound, "not_found", "Not found")
})

// spaOptions personalise the app's index.html and its CSP.
func (d Deps) spaOptions() spaOptions {
	in := d.Config.Instance
	return spaOptions{Title: in.Name, Theme: in.Theme, LogoOrigin: in.LogoOrigin()}
}

// NewAPIHandler serves the full application surface and the web app.
func NewAPIHandler(d Deps) http.Handler {
	var fallback http.Handler = notFound
	if d.Web != nil {
		fallback = spaHandler(d.Web, d.WebBuilt, d.spaOptions())
	}
	return build(d, apiRoutes(d), fallback)
}
