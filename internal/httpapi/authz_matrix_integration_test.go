package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/mcp"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// The authorization matrix. Every operation in api/openapi.yaml is
// classified in matrixPolicies (authz_matrix_policy_test.go); this test calls
// every operation as every kind of caller:
//
//   - on the caller's own team (B), or globally: success exactly for the
//     classified callers, and 401, 403 or 404 for everyone else;
//   - on team A, of which the team-role callers are not members: denied for
//     everyone but the classified platform roles, and never with team A's
//     name or ID in a denied response.

// teamFix is one team's fixture.
type teamFix struct {
	slug, id, name string
	member         string // a member (role member) the callers are not, for update/remove
	invite         string // an open invite
	auditID        string // an entry of the team's audit log
	// serviceAccount is a service account with one key (saKey).
	serviceAccount, saKey string
	// owner is an owner of the team; agent a team agent with a thread;
	// template a private team template.
	owner, agent, thread, template string
	// provider is a model provider of the team's own, and model one of its
	// models; mcpServer an MCP server of the team's own.
	provider, model, mcpServer string
}

type matrixEnv struct {
	t       *testing.T
	app     *testApp
	callers [numCallers]*session
	userIDs [numCallers]string
	a, b    teamFix
	// spareUser and spareTeam are targets of platform writes that are
	// refused; allowed writes get fresh ones.
	spareUser, spareTeam string
	// provider and model: a model provider and one of its models;
	// upstream serves the provider's model list.
	provider, model, upstream, mcpServer string
	personal                             string
	// oauthClient is a registered OAuth Connect client; spareGrant one of
	// spareUser's connected apps.
	oauthClient, spareGrant string
	// logoURL is the logo of oauthClient (an https server on this machine).
	logoURL string
	n       int
}

func (e *matrixEnv) next() int { e.n++; return e.n }

func (e *matrixEnv) exec(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.app.Pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// newUser inserts a user (as a sign-in would) and returns its ID.
func (e *matrixEnv) newUser(email, role string) string {
	return e.exec(`INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, platform_role)
		VALUES ('matrix', $1, $2, $3, $4) RETURNING id::text`, email, email, email, role)
}

// sessionFor creates a browser session for a user directly in the database
// (the matrix needs more people than the development personas).
func (e *matrixEnv) sessionFor(userID string) *session {
	e.t.Helper()
	secret, csrf := httpx.NewID(32), httpx.NewID(32)
	e.exec(`INSERT INTO sessions (user_id, token_digest, csrf_token, expires_at)
		VALUES ($1, $2, $3, now() + interval '1 hour') RETURNING id::text`, userID, auth.Digest(secret), csrf)
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(mustParse(e.t, e.app.URL), []*http.Cookie{{Name: auth.SessionCookieName(false), Value: secret, Path: "/"}})
	return &session{t: e.t, app: e.app, client: &http.Client{Jar: jar}, csrf: csrf}
}

func anonSession(t *testing.T, app *testApp) *session {
	jar, _ := cookiejar.New(nil)
	return &session{t: t, app: app, client: &http.Client{Jar: jar}}
}

func (e *matrixEnv) newTeam(slug, name string) teamFix {
	tf := teamFix{slug: slug, name: name}
	tf.id = e.exec(`INSERT INTO teams (slug, name) VALUES ($1, $2) RETURNING id::text`, slug, name)
	tf.member = e.newMember(tf, "member")
	tf.invite = e.newInvite(tf)
	tf.auditID = e.exec(`INSERT INTO audit_log (actor_kind, team_id, action, target_type, target_id)
		VALUES ('system', $1::uuid, 'team.create', 'team', $1::text) RETURNING id::text`, tf.id)
	tf.serviceAccount = e.newServiceAccount(tf.id)
	tf.saKey = e.newServiceAccountKey(tf.serviceAccount)
	return tf
}

// newServiceAccount inserts a service account of a team.
func (e *matrixEnv) newServiceAccount(teamID string) string {
	return e.exec(`INSERT INTO service_accounts (team_id, name) VALUES ($1, $2) RETURNING id::text`, teamID, fmt.Sprintf("sa-%d", e.next()))
}

// newServiceAccountKey inserts a key of a service account.
func (e *matrixEnv) newServiceAccountKey(saID string) string {
	return e.exec(`INSERT INTO service_account_keys (service_account_id, name, prefix, digest, pepper_id, expires_at)
		VALUES ($1, 'k', 'pdp_sa_matrix00', encode(sha256($2::bytea), 'hex'), decode('0000000000000000', 'hex'),
		        now() + interval '1 day') RETURNING id::text`,
		saID, fmt.Sprintf("sa-key-%d", e.next()))
}

func (e *matrixEnv) newMember(tf teamFix, role string) string {
	id := e.newUser(fmt.Sprintf("m-%d@example.com", e.next()), "none")
	e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3) RETURNING role`, tf.id, id, role)
	return id
}

func (e *matrixEnv) newInvite(tf teamFix) string {
	return e.exec(`INSERT INTO team_invites (team_id, email, role, expires_at)
		VALUES ($1, $2, 'member', now() + interval '1 day') RETURNING id::text`, tf.id, fmt.Sprintf("i-%d@example.com", e.next()))
}

func newMatrixEnv(t *testing.T) *matrixEnv {
	logoHost := newLogoHost(t)
	e := &matrixEnv{t: t, logoURL: logoHost.srv.URL + "/logo.png",
		app: newTestAppDeps(t, nil, testutil.Logger(), func(d *httpapi.Deps) { d.OAuth.SetMetadataTLS(logoHost.tls()) })}
	e.a = e.newTeam("alpha", "Alpha Confidential Team")
	e.b = e.newTeam("bravo", "Bravo Team")
	e.a.owner = e.newMember(e.a, "owner")
	roles := map[caller]string{cMember: "member", cEditor: "editor", cTeamAdmin: "admin", cOwner: "owner"}
	platformRoles := map[caller]string{cPlatformAdmin: "platform_admin", cAuditor: "platform_auditor"}
	for c := cMember; c < numCallers; c++ {
		prole := platformRoles[c]
		if prole == "" {
			prole = "none"
		}
		id := e.newUser(fmt.Sprintf("%s@example.com", c), prole)
		if r, ok := roles[c]; ok {
			e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, $3) RETURNING role`, e.b.id, id, r)
		}
		e.userIDs[c] = id
		e.callers[c] = e.sessionFor(id)
	}
	e.callers[cAnon] = anonSession(t, e.app)
	e.spareUser = e.newUser("spare@example.com", "none")
	e.spareTeam = e.newTeam("spare", "Spare Team").slug
	e.b.owner = e.userIDs[cOwner]
	for _, tf := range []*teamFix{&e.a, &e.b} {
		tf.agent = e.newAgent(*tf)
		tf.thread = e.newThread(*tf, tf.agent)
		tf.template = e.newTemplate(*tf, "private")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"matrix-model"}]}`))
	}))
	t.Cleanup(upstream.Close)
	e.upstream = upstream.URL
	// Teams' providers live at matrixTeamProviderURL: a public address, as
	// the address guard sees it, served by a local https server.
	teamUpstream := httptest.NewTLSServer(upstream.Config.Handler)
	t.Cleanup(teamUpstream.Close)
	useTeamNetwork(t, e.app, teamUpstream, map[string]string{"models.matrix.test": "93.184.216.34", "mcp.matrix.test": "93.184.216.34"})
	for _, tf := range []*teamFix{&e.a, &e.b} {
		tf.provider = e.newTeamProvider(*tf)
		tf.model = e.newTeamModel(*tf)
		tf.mcpServer = e.newTeamMCPServer(*tf)
	}
	e.provider = e.newProvider()
	e.model = e.newModel(e.provider)
	e.mcpServer = e.newMCPServer()
	e.oauthClient = e.newOAuthClient()
	e.spareGrant = e.newOAuthGrant(e.spareUser)
	return e
}

// newOAuthClient registers an OAuth Connect client (as Dynamic Client
// Registration would) with a loopback redirect URI.
func (e *matrixEnv) newOAuthClient() string {
	id := fmt.Sprintf("pdpcl_matrix%d", e.next())
	return e.exec(`INSERT INTO oauth_clients (client_id, client_name, redirect_uris, logo_uri) VALUES ($1, 'Matrix app', '{http://127.0.0.1/cb}', $2)
		RETURNING client_id`, id, e.logoURL)
}

// consentQuery is an authorization request for the matrix's client.
func (e *matrixEnv) consentQuery() string {
	return "client_id=" + e.oauthClient + "&redirect_uri=http%3A%2F%2F127.0.0.1%2Fcb&response_type=code&code_challenge_method=S256" +
		"&code_challenge=" + strings.Repeat("A", 43) + "&state=s"
}

// newOAuthGrant is a connected app of a person (each with its own client,
// since a person has one active grant per client).
func (e *matrixEnv) newOAuthGrant(userID string) string {
	return e.exec(`INSERT INTO oauth_grants (user_id, client_id, client_kind, client_name, resource)
		VALUES ($1, $2, 'registered', 'Matrix app', $3) RETURNING id::text`, userID, fmt.Sprintf("pdpcl_grant%d", e.next()), e.app.URL)
}

// newMCPServer adds an MCP server (its tools can't be read: the upstream is
// not an MCP server), as a platform admin.
func (e *matrixEnv) newMCPServer() string {
	e.t.Helper()
	v, err := e.app.MCP.CreateServer(context.Background(), e.platformAdmin(), mcp.ServerInput{
		Name: fmt.Sprintf("mcp-server-%d", e.next()), URL: e.upstream + "/mcp"})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Server.ID.String()
}

func (e *matrixEnv) platformAdmin() authz.Actor {
	return authz.Actor{UserID: uuid.MustParse(e.userIDs[cPlatformAdmin]), PlatformRole: authz.PlatformAdmin}
}

// newProvider adds a model provider, as a platform admin.
func (e *matrixEnv) newProvider() string {
	e.t.Helper()
	v, err := e.app.Models.CreateProvider(context.Background(), e.platformAdmin(), llm.ProviderInput{
		Name: fmt.Sprintf("provider-%d", e.next()), Kind: "openai-compatible", BaseURL: e.upstream + "/v1", APIKey: "matrix-key"})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Provider.ID.String()
}

// newModel adds a model to a provider, as a platform admin.
func (e *matrixEnv) newModel(provider string) string {
	e.t.Helper()
	v, err := e.app.Models.CreateModel(context.Background(), e.platformAdmin(), uuid.MustParse(provider),
		llm.ModelInput{UpstreamID: fmt.Sprintf("model-%d", e.next())})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Model.ID.String()
}

// matrixTeamProviderURL is where teams' providers are (see useTeamNetwork).
const matrixTeamProviderURL = "https://models.matrix.test/v1"

// newTeamProvider adds a model provider of the team's own, as its owner.
func (e *matrixEnv) newTeamProvider(tf teamFix) string {
	e.t.Helper()
	actor := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	v, err := e.app.Models.CreateTeamProvider(context.Background(), actor, tf.slug, llm.ProviderInput{
		Name: fmt.Sprintf("team-provider-%d", e.next()), Kind: "openai-compatible", BaseURL: matrixTeamProviderURL, APIKey: "matrix-key"})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Provider.ID.String()
}

// matrixTeamMCPURL is where teams' MCP servers are (see useTeamNetwork; not
// an MCP server, so their tools can't be read).
const matrixTeamMCPURL = "https://mcp.matrix.test/mcp"

// newTeamMCPServer adds an MCP server of the team's own, as its owner.
func (e *matrixEnv) newTeamMCPServer(tf teamFix) string {
	e.t.Helper()
	actor := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	v, err := e.app.MCP.CreateTeamServer(context.Background(), actor, tf.slug, mcp.ServerInput{
		Name: fmt.Sprintf("team-mcp-%d", e.next()), URL: matrixTeamMCPURL})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Server.ID.String()
}

// newTeamModel adds a model to the team's fixture provider, as its owner.
func (e *matrixEnv) newTeamModel(tf teamFix) string {
	e.t.Helper()
	actor := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	v, err := e.app.Models.CreateTeamModel(context.Background(), actor, tf.slug, uuid.MustParse(tf.provider),
		llm.ModelInput{UpstreamID: fmt.Sprintf("team-model-%d", e.next())})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Model.ID.String()
}

// newTemplate creates a team template, as the team's owner, in a status.
func (e *matrixEnv) newTemplate(tf teamFix, status string) string {
	e.t.Helper()
	actor := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	v, err := e.app.Agents.CreateTemplate(context.Background(), actor, tf.slug, agents.TemplateInput{
		Slug: fmt.Sprintf("tpl-%d", e.next()), Name: "Template", Instructions: "Be helpful."})
	if err != nil {
		e.t.Fatal(err)
	}
	if status != "private" {
		e.exec(`UPDATE agent_templates SET status = $2 WHERE id = $1 RETURNING status`, v.Template.ID, status)
	}
	return v.Template.ID.String()
}

// newAgent creates a team agent, as the team's owner.
func (e *matrixEnv) newAgent(tf teamFix) string {
	e.t.Helper()
	actor := authz.Actor{UserID: uuid.MustParse(tf.owner), PlatformRole: authz.PlatformNone}
	v, err := e.app.Agents.CreateTeamAgent(context.Background(), actor, tf.slug, agents.CreateInput{Name: fmt.Sprintf("agent-%d", e.next())})
	if err != nil {
		e.t.Fatal(err)
	}
	return v.Agent.ID.String()
}

// newThread starts a conversation with a team agent, as the team's owner
// (this starts the agent's runtime).
func (e *matrixEnv) newThread(tf teamFix, agentID string) string {
	e.t.Helper()
	thread := fmt.Sprintf("t-%d", e.next())
	code, body := e.sessionFor(tf.owner).raw("POST", "/v1/agents/"+agentID+"/threads/"+thread+"/messages", map[string]any{"text": "hello"}, nil)
	if code != http.StatusAccepted {
		e.t.Fatalf("start thread: %d %s", code, body)
	}
	return thread
}

// mctx is what a request builder sees.
type mctx struct {
	e       *matrixEnv
	tf      *teamFix
	who     caller
	allowed bool // expected to succeed: destructive calls get fresh objects
}

func (c *mctx) team(suffix string) string { return "/v1/teams/" + c.tf.slug + suffix }

// agent is the team's agent path, or a fresh agent's when a destructive call
// is expected to succeed.
func (c *mctx) agent(suffix string, fresh bool) string {
	id := c.tf.agent
	if fresh && c.allowed {
		id = c.e.newAgent(*c.tf)
	}
	return "/v1/agents/" + id + suffix
}

// template is the team template's path, or a fresh one's (in status) for a
// call expected to succeed.
func (c *mctx) template(suffix string, fresh bool, status string) string {
	id := c.tf.template
	if fresh && c.allowed {
		id = c.e.newTemplate(*c.tf, status)
	}
	return "/v1/agent-templates/" + id + suffix
}

// thread is the team agent's thread path, or a fresh thread's for a
// destructive call expected to succeed.
func (c *mctx) thread(suffix string, fresh bool) string {
	t := c.tf.thread
	if fresh && c.allowed {
		t = c.e.newThread(*c.tf, c.tf.agent)
	}
	return "/v1/agents/" + c.tf.agent + "/threads/" + t + suffix
}

// adminThread is the team agent's thread under the platform admins' routes
// (the thread is shared; private threads are covered by
// private_threads_integration_test.go).
func (c *mctx) adminThread(suffix string) string {
	return "/v1/admin/agents/" + c.tf.agent + "/threads/" + c.tf.thread + suffix
}

// provider is the fixture provider, or a fresh one for a destructive call
// expected to succeed.
func (c *mctx) provider(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newProvider()
	}
	return c.e.provider
}

// model is the fixture model, or a fresh one for a destructive call expected
// to succeed.
func (c *mctx) model(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newModel(c.e.provider)
	}
	return c.e.model
}

// teamProvider is the team's fixture provider, or a fresh one for a
// destructive call expected to succeed.
func (c *mctx) teamProvider(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newTeamProvider(*c.tf)
	}
	return c.tf.provider
}

// teamMCPServer is the team's fixture MCP server, or a fresh one for a
// destructive call expected to succeed.
func (c *mctx) teamMCPServer(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newTeamMCPServer(*c.tf)
	}
	return c.tf.mcpServer
}

// teamModel is the team's fixture model, or a fresh one for a destructive
// call expected to succeed.
func (c *mctx) teamModel(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newTeamModel(*c.tf)
	}
	return c.tf.model
}

// personalTeam is a personal workspace (a fresh one when the call is
// expected to succeed: the override changes its revision).
func (c *mctx) personalTeam() string {
	if c.allowed || c.e.personal == "" {
		owner := c.e.newUser(fmt.Sprintf("personal-%d@example.com", c.e.next()), "none")
		slug := fmt.Sprintf("personal-m%d", c.e.next())
		id := c.e.exec(`INSERT INTO teams (slug, name, kind, personal_owner) VALUES ($1, 'Personal', 'personal', $2) RETURNING id::text`, slug, owner)
		c.e.exec(`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'owner') RETURNING role`, id, owner)
		if !c.allowed {
			c.e.personal = slug
		}
		return slug
	}
	return c.e.personal
}

// mcpServer is the fixture MCP server, or a fresh one for a destructive call
// expected to succeed.
func (c *mctx) mcpServer(fresh bool) string {
	if fresh && c.allowed {
		return c.e.newMCPServer()
	}
	return c.e.mcpServer
}

func (c *mctx) member() string {
	if c.allowed {
		return c.e.newMember(*c.tf, "member")
	}
	return c.tf.member
}

func (c *mctx) invite() string {
	if c.allowed {
		return c.e.newInvite(*c.tf)
	}
	return c.tf.invite
}

// apiKey is a fresh key of the caller when the call is expected to
// succeed, else another person's key.
func (c *mctx) apiKey() string {
	owner := c.e.spareUser
	if c.allowed {
		owner = c.e.userIDs[c.who]
	}
	n := c.e.next()
	return c.e.exec(`INSERT INTO api_keys (user_id, name, prefix, digest, pepper_id, expires_at)
		VALUES ($1, 'k', 'pdp_matrix00', encode(sha256($2::bytea), 'hex'), decode('0000000000000000', 'hex'),
		        now() + interval '1 day') RETURNING id::text`,
		owner, fmt.Sprintf("matrix-%d", n))
}

// serviceAccount is a fresh service account of the team when the call is
// expected to succeed, else the team's fixture one.
func (c *mctx) serviceAccount() string {
	if c.allowed {
		return c.e.newServiceAccount(c.tf.id)
	}
	return c.tf.serviceAccount
}

// serviceAccountKey is a service account and its key: fresh ones when the
// call is expected to succeed.
func (c *mctx) serviceAccountKey() (sa, key string) {
	if c.allowed {
		sa = c.e.newServiceAccount(c.tf.id)
		return sa, c.e.newServiceAccountKey(sa)
	}
	return c.tf.serviceAccount, c.tf.saKey
}

// myGrant is a fresh connected app of the caller when the call is expected
// to succeed, else another person's.
func (c *mctx) myGrant() string {
	if c.allowed {
		return c.e.newOAuthGrant(c.e.userIDs[c.who])
	}
	return c.e.spareGrant
}

// spareGrant is a fresh connected app of spareUser when the call is
// expected to succeed, else the fixture one.
func (c *mctx) spareGrant() string {
	if c.allowed {
		return c.e.newOAuthGrant(c.e.spareUser)
	}
	return c.e.spareGrant
}

func (c *mctx) user() string {
	if c.allowed {
		return c.e.newUser(fmt.Sprintf("u-%d@example.com", c.e.next()), "none")
	}
	return c.e.spareUser
}

func (c *mctx) spareTeam() string {
	if c.allowed {
		return c.e.newTeam(fmt.Sprintf("fresh-%d", c.e.next()), "Fresh").slug
	}
	return c.e.spareTeam
}

// call runs one request as who and checks the outcome.
func (e *matrixEnv) call(t *testing.T, op string, p policy, tf *teamFix, who caller, allowed, foreign bool) {
	t.Helper()
	c := &mctx{e: e, tf: tf, who: who, allowed: allowed}
	req := p.build(c)
	s := e.callers[who]
	switch {
	case req.anon:
		s = anonSession(t, e.app)
	case req.ownSession && who != cAnon:
		s = e.sessionFor(e.userIDs[who])
	}
	code, body := s.raw(req.method, req.path, req.body, req.headers)
	where := "own"
	if foreign {
		where = "team A"
	}
	label := fmt.Sprintf("%s as %s on %s (%s %s)", op, who, where, req.method, req.path)
	switch {
	case code >= 500:
		t.Errorf("%s: %d %s", label, code, body)
	case p.anyStatus:
	case allowed && (code < 200 || code > 299):
		t.Errorf("%s: want success, got %d %s", label, code, body)
	case !allowed && code != 401 && code != 403 && code != 404:
		t.Errorf("%s: want 401/403/404, got %d %s", label, code, body)
	}
	if foreign && !allowed && (strings.Contains(string(body), e.a.id) || strings.Contains(string(body), e.a.name)) {
		t.Errorf("%s: denied response leaks team A: %s", label, body)
	}
}

func TestAuthorizationMatrix(t *testing.T) {
	e := newMatrixEnv(t)
	ops := make([]string, 0, len(matrixPolicies))
	for op := range matrixPolicies {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		p := matrixPolicies[op]
		t.Run(op, func(t *testing.T) {
			if p.scope == scopeGlobal {
				for who := caller(0); who < numCallers; who++ {
					e.call(t, op, p, &e.b, who, p.own.has(who), false)
				}
				return
			}
			for _, who := range []caller{cAnon, cMember, cEditor, cTeamAdmin, cOwner} {
				e.call(t, op, p, &e.b, who, p.own.has(who), false)
			}
			for who := caller(0); who < numCallers; who++ {
				e.call(t, op, p, &e.a, who, p.foreign.has(who), true)
			}
		})
	}
}
