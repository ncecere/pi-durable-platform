// Package httpapi assembles the HTTP surface: the route table, middleware,
// operational endpoints and the /v1 handlers.
//
// Every route is declared in a table (apiRoutes, grouped by area) so tests
// can verify that the served surface matches api/openapi.yaml exactly.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apikeys"
	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/mcpidentity"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/oauth"
	"github.com/ncecere/pi-durable-platform/internal/platform"
	"github.com/ncecere/pi-durable-platform/internal/ratelimit"
	"github.com/ncecere/pi-durable-platform/internal/secrets"
	"github.com/ncecere/pi-durable-platform/internal/serviceaccounts"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
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
	// OAuth is OAuth Connect: the authorization server whose tokens let an
	// app act as the person who connected it.
	OAuth *oauth.Service
	// Agents manages agents and relays conversations to their runtimes.
	Agents *agents.Service
	// Models is the model catalog and the model proxy.
	Models *llm.Service
	// MCP is the MCP catalog, apps' thread servers and the MCP proxy.
	MCP *mcp.Service
	// MCPIdentity signs the identity tokens the MCP proxy sends servers,
	// and publishes its keys (nil: none).
	MCPIdentity *mcpidentity.Keys
	// Triggers are agents' schedules and webhooks, and their runs.
	Triggers *triggers.Service
	// Secrets seals values stored in the database with ENCRYPTION_KEY
	// (nil in tests that don't need it).
	Secrets *secrets.Box
	// Metrics counts the requests of both listeners (nil: none); the other
	// services hold it too (app.NewDeps).
	Metrics *metrics.Metrics
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
	// limiter counts the OAuth protocol endpoints' requests per address:
	// RateStore, or in memory without one.
	limiter ratelimit.Store
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
	a := &api{Deps: d, limiter: d.RateStore}
	if d.Pool != nil {
		a.q = dbgen.New(d.Pool)
	}
	if a.limiter == nil {
		a.limiter = ratelimit.New()
	}
	routes := opsRoutes(d)
	// Add a new area's route groups here (and its paths to api/openapi.yaml).
	for _, group := range [][]route{a.authRoutes(), a.teamRoutes(), a.platformAdminRoutes(), a.apiKeyRoutes(),
		a.serviceAccountRoutes(), a.serviceAccountAdminRoutes(), a.agentRoutes(), a.approvalRoutes(), a.templateRoutes(), a.modelRoutes(), a.teamModelRoutes(), a.mcpRoutes(), a.personalRoutes(),
		a.oauthRoutes(), a.oauthRESTRoutes(), a.oauthAppRoutes(), a.mcpIdentityRoutes(), a.triggerRoutes()} {
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
		{"GET", "/v1/teams", a.person(a.listMyTeams)},
		{"GET", "/v1/teams/{team}", a.person(a.getTeam)},
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

// NewGatewayHandler is what agents call, on its own listener
// (AGENTS_GATEWAY_ADDR): the model proxy, usage reports, the MCP proxy and
// their threads' MCP servers, thread status and tool approval requests.
func NewGatewayHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	log := d.Log.With("component", "gateway")
	d.Models.RegisterGateway(mux, d.Agents.AuthenticateProxy, log)
	d.MCP.RegisterGateway(mux, d.Agents.AuthenticateMCP, d.Config.Agents.GatewayURL, llm.Credential, log)
	registerThreadReports(mux, d.Agents, log)
	registerApprovalReports(mux, d.Agents, log)
	if d.Triggers != nil {
		registerSchedules(mux, d.Agents, d.Triggers, log)
	}
	return observeHandler(d.Metrics, metrics.ListenerGateway, mux)
}

// registerThreadReports takes the runtime's reports: its threads' status, one
// thread (/threads/report) or all of them when it starts (/threads/sync), and
// its versions when it starts, before it opens the agent's storage
// (/runtime/start: the upgrade check, which answers 409 runtime_too_old to a
// runtime older than the one that last wrote the agent's data).
func registerThreadReports(mux *http.ServeMux, ag *agents.Service, log *slog.Logger) {
	authed := func(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
		id, token, ok := llm.Credential(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "A gateway credential is required")
			return uuid.Nil, false
		}
		if _, _, err := ag.AuthenticateProxy(r.Context(), id, token); err != nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized", "Invalid gateway credential")
			return uuid.Nil, false
		}
		return id, true
	}
	mux.HandleFunc("POST /threads/report", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in agents.ThreadReport
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be a thread report")
			return
		}
		if failed(w, r, ag.ReportThread(r.Context(), id, in)) {
			return
		}
		httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
	})
	mux.HandleFunc("POST /runtime/start", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in struct {
			ContractVersion  json.RawMessage `json:"contractVersion"`
			PiDurableVersion json.RawMessage `json:"piDurableVersion"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be the runtime's versions")
			return
		}
		var version string // a version of another type counts as none
		_ = json.Unmarshal(in.PiDurableVersion, &version)
		err := ag.AdmitRuntime(r.Context(), id, version)
		var refused *agenthost.RefusedError
		if errors.As(err, &refused) {
			httpx.Error(w, http.StatusConflict, "runtime_too_old", refused.Message)
			return
		}
		if err != nil {
			log.Warn("runtime start check", "agent", id, "err", err)
		}
		if failed(w, r, err) {
			return
		}
		httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
	})
	mux.HandleFunc("POST /threads/sync", func(w http.ResponseWriter, r *http.Request) {
		id, ok := authed(w, r)
		if !ok {
			return
		}
		var in struct {
			Threads []agents.ThreadReport `json:"threads"`
			At      int64                 `json:"at"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&in); err != nil || len(in.Threads) > 100_000 {
			httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be the runtime's threads")
			return
		}
		if err := ag.SyncThreads(r.Context(), id, in.Threads, in.At); err != nil {
			log.Warn("sync threads", "agent", id, "err", err)
			failed(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
	})
}
