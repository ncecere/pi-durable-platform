package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/auth"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
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
	// owner is an owner of the team; agent a team agent with a thread.
	owner, agent, thread string
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
	n                    int
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
	e := &matrixEnv{t: t, app: newTestApp(t, nil)}
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
	}
	return e
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

// thread is the team agent's thread path, or a fresh thread's for a
// destructive call expected to succeed.
func (c *mctx) thread(suffix string, fresh bool) string {
	t := c.tf.thread
	if fresh && c.allowed {
		t = c.e.newThread(*c.tf, c.tf.agent)
	}
	return "/v1/agents/" + c.tf.agent + "/threads/" + t + suffix
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
