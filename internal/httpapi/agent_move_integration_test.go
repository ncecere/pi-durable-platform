package httpapi_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

const destTeam = "/v1/teams/dest"

// moveEnv: team tools (user owner, alex editor, blair member, a chat app's
// editor service account) and team dest (user owner, alex admin, casey
// member), with personal workspaces on.
type moveEnv struct {
	t                        *testing.T
	app                      *testApp
	admin, auditor           *session
	user, alex, blair, casey *session
	chat                     sender
	userWS                   string
}

func newMoveEnv(t *testing.T) *moveEnv { return newMoveEnvWith(t, nil) }

// newMoveEnvWith is newMoveEnv with a configuration change.
func newMoveEnvWith(t *testing.T, mutate func(*config.Config)) *moveEnv {
	app := newTestAppWith(t, mutate, testutil.Logger(), nil, true)
	e := &moveEnv{t: t, app: app, admin: app.signIn("admin"), auditor: app.signIn("auditor"), user: app.signIn("user"),
		alex: app.signIn("alex"), blair: app.signIn("blair"), casey: app.signIn("casey")}
	createTeam(t, e.admin, "tools", "user@localhost")
	createTeam(t, e.admin, "dest", "user@localhost")
	for _, m := range []struct{ team, email, role string }{
		{toolsTeam, "alex@localhost", "editor"}, {toolsTeam, "blair@localhost", "member"},
		{destTeam, "alex@localhost", "admin"}, {destTeam, "casey@localhost", "member"},
	} {
		code, ec := e.user.call("POST", m.team+"/members", map[string]any{"email": m.email, "role": m.role}, nil, nil)
		mustCode(t, "add "+m.email, code, ec, 201, "")
	}
	e.chat = viaBearer(createSAKey(t, e.user, createSA(t, e.user, map[string]any{"name": "chat-app", "role": "editor"}).Id.String(),
		map[string]any{"name": "k"}).Secret)
	e.userWS = "/v1/teams/" + personalSlug(t, e.user)
	return e
}

func (e *moveEnv) agent(team, name string, body map[string]any) apitypes.Agent {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	body["name"] = name
	var ag apitypes.Agent
	code, ec := e.user.call("POST", team+"/agents", body, &ag, nil)
	mustCode(e.t, "create "+name, code, ec, 201, "")
	return ag
}

func (e *moveEnv) plan(s *session, agentID uuid.UUID, team string) (apitypes.AgentMovePlan, int, string) {
	e.t.Helper()
	var p apitypes.AgentMovePlan
	code, ec := s.call("GET", "/v1/agents/"+agentID.String()+"/move?team="+url.QueryEscape(team), nil, &p, nil)
	return p, code, ec
}

func blockers(p apitypes.AgentMovePlan) map[string]bool {
	out := map[string]bool{}
	for _, b := range p.Blockers {
		out[b.Code] = true
	}
	return out
}

// An agent moved to another team keeps its conversations and is usable only
// under the new team's rules: shared threads go with it, private ones stay
// their creator's, the old team's people and service accounts lose it, and
// the move is audited in both teams.
func TestAgentMove(t *testing.T) {
	e := newMoveEnv(t)
	ag := e.agent(toolsTeam, "Mover", nil)
	base := "/v1/agents/" + ag.Id.String()

	// Conversations before: a shared one, a member's private one, the chat app's private one.
	code, ec := e.user.call("POST", base+"/threads/s1/messages", map[string]any{"text": "remember 42"}, nil, nil)
	mustCode(t, "shared thread", code, ec, 202, "")
	code, ec = e.blair.call("POST", base+"/threads/p1/messages", map[string]any{"text": "my secret", "visibility": "private"}, nil, nil)
	mustCode(t, "private thread", code, ec, 202, "")
	if r := send(t, e.app, e.chat, "POST", base+"/threads/sa1/messages", map[string]any{"text": "app", "visibility": "private"}, nil); r.code != 202 {
		t.Fatalf("service account's thread: %+v", r)
	}
	before := waitReply(t, e.user, base+"/threads/s1", 2)
	waitReply(t, e.blair, base+"/threads/p1", 2)
	// Usage recorded in the old team (as the gateway records it).
	if _, err := e.app.Pool.Exec(context.Background(), `INSERT INTO model_usage (team_id, agent_id, source, input_tokens)
		VALUES ($1, $2, 'agent', 10)`, ag.TeamId, ag.Id); err != nil {
		t.Fatal(err)
	}

	// The dry run: who may ask, and what it says.
	p, code, ec := e.plan(e.user, ag.Id, "dest")
	mustCode(t, "plan", code, ec, 200, "")
	if !p.Allowed || len(p.Blockers) != 0 || p.From.Slug != "tools" || p.To.Slug != "dest" || p.SharedThreads != 1 || p.PrivateThreads != 2 ||
		p.PrivateThreadsUnreachable != 2 || p.TargetMembers != 3 || p.TargetServiceAccounts != 0 || p.PeopleLosingAccess != 1 ||
		p.ServiceAccountsLosingAccess != 1 || p.ExternalKey != nil || p.ThreadMcpServers != 0 || len(p.McpServersDropped) != 0 || p.Model != nil {
		t.Fatalf("plan %+v", p)
	}
	for _, c := range []struct {
		who      *session
		team     string
		code     int
		errCode  string
		describe string
	}{
		{e.alex, "dest", 403, "forbidden", "an editor of its team (admin of the target)"},
		{e.blair, "dest", 404, "team_not_found", "a member, not on the target"},
		{e.casey, "dest", 404, "agent_not_found", "a member of the target only"},
		{e.auditor, "dest", 403, "forbidden", "a platform auditor"},
		{e.user, "tools", 409, "move_same_team", "to its own team"},
		{e.user, "nowhere", 404, "team_not_found", "to no team"},
	} {
		_, code, ec := e.plan(c.who, ag.Id, c.team)
		mustCode(t, "plan by "+c.describe, code, ec, c.code, c.errCode)
	}
	if _, code, ec := e.plan(e.admin, ag.Id, "dest"); code != 200 {
		t.Fatalf("a platform admin plans a move between teams they aren't on: %d %s", code, ec)
	}
	if r := send(t, e.app, e.chat, "GET", base+"/move?team=dest", nil, nil); r.code != 403 || r.err != "person_required" {
		t.Fatalf("a service account plans: %+v", r)
	}

	// The move: If-Match, then done.
	code, ec = e.user.call("POST", base+"/move", map[string]any{"team": "dest"}, nil, nil)
	mustCode(t, "move without If-Match", code, ec, 428, "")
	code, ec = e.alex.call("POST", base+"/move", map[string]any{"team": "dest"}, nil, ifMatch(ag.Revision))
	mustCode(t, "move by an editor", code, ec, 403, "forbidden")
	code, ec = e.user.call("POST", base+"/move", map[string]any{"team": "dest"}, nil, ifMatch(ag.Revision+5))
	mustCode(t, "move with a stale revision", code, ec, 412, "")
	var moved apitypes.Agent
	code, ec = e.user.call("POST", base+"/move", map[string]any{"team": "dest"}, &moved, ifMatch(ag.Revision))
	mustCode(t, "move", code, ec, 200, "")
	if moved.Id != ag.Id || moved.TeamSlug != "dest" || moved.Revision != ag.Revision+1 || moved.Running {
		t.Fatalf("moved %+v", moved)
	}

	// Its conversations came along: read after, and it carries on.
	after := waitReply(t, e.user, base+"/threads/s1", 2)
	if len(after.Messages) != len(before.Messages) || text(after.Messages[0]) != text(before.Messages[0]) {
		t.Fatalf("conversation before %+v, after %+v", before.Messages, after.Messages)
	}
	code, ec = e.user.call("POST", base+"/threads/s1/messages", map[string]any{"text": "still there?"}, nil, nil)
	mustCode(t, "send after the move", code, ec, 202, "")
	waitReply(t, e.user, base+"/threads/s1", 4)

	// The new team's members use it; private threads stay their creator's.
	var listed []apitypes.AgentThread
	if code := e.casey.get(base+"/threads", &listed); code != 200 || len(listed) != 1 || listed[0].ThreadId != "s1" {
		t.Fatalf("the new team's member lists %d %+v", code, listed)
	}
	if code := e.casey.get(base+"/threads/s1", nil); code != 200 {
		t.Fatalf("the new team's member reads the shared thread: %d", code)
	}
	code, ec = e.casey.call("GET", base+"/threads/p1", nil, nil, nil)
	mustCode(t, "the new team's member reads another's private thread", code, ec, 404, "thread_not_found")
	code, ec = e.alex.call("GET", base+"/threads/p1", nil, nil, nil)
	mustCode(t, "the new team's admin reads another's private thread", code, ec, 404, "thread_not_found")

	// The old team's people and service accounts lose it.
	code, ec = e.blair.call("GET", base, nil, nil, nil)
	mustCode(t, "the old team's member gets the agent", code, ec, 404, "agent_not_found")
	code, ec = e.blair.call("GET", base+"/threads/p1", nil, nil, nil)
	mustCode(t, "the old team's member reads their private thread", code, ec, 404, "agent_not_found")
	if r := send(t, e.app, e.chat, "GET", base, nil, nil); r.code != 404 || r.err != "agent_not_found" {
		t.Fatalf("the old team's service account: %+v", r)
	}
	var tools, dest []apitypes.Agent
	e.user.get(toolsTeam+"/agents", &tools)
	e.user.get(destTeam+"/agents", &dest)
	if len(tools) != 0 || len(dest) != 1 || dest[0].Id != ag.Id {
		t.Fatalf("tools %+v, dest %+v", tools, dest)
	}

	// Usage stays with the team it happened in.
	var toolsUsage, destUsage apitypes.UsageReport
	e.user.get(toolsTeam+"/usage?groupBy=agent", &toolsUsage)
	e.user.get(destTeam+"/usage?groupBy=agent", &destUsage)
	if toolsUsage.Total.InputTokens != 10 || destUsage.Total.InputTokens != 0 {
		t.Fatalf("usage: tools %+v, dest %+v", toolsUsage.Total, destUsage.Total)
	}

	// Audited in both teams' logs.
	for _, team := range []string{toolsTeam, destTeam} {
		entry := findAudit(t, auditPage(t, e.user, team+"/audit", url.Values{}), "agent.move", ag.Id.String())
		if entry.Metadata["fromTeam"] != "tools" || entry.Metadata["toTeam"] != "dest" {
			t.Fatalf("%s: %+v", team, entry)
		}
	}
}

// What a team needs to take an agent: its template, its model, a free name;
// service-managed agents stay; personal workspaces are their owner's.
func TestAgentMoveRefusals(t *testing.T) {
	e := newMoveEnv(t)
	ctx := context.Background()
	refused := func(ag apitypes.Agent, team, code string, body map[string]any) {
		t.Helper()
		p, c, ec := e.plan(e.user, ag.Id, team)
		mustCode(t, "plan "+ag.Name, c, ec, 200, "")
		if p.Allowed || !blockers(p)[code] {
			t.Fatalf("%s: plan %+v, want blocker %s", ag.Name, p, code)
		}
		if body == nil {
			body = map[string]any{}
		}
		body["team"] = team
		c, ec = e.user.call("POST", "/v1/agents/"+ag.Id.String()+"/move", body, nil, ifMatch(ag.Revision))
		mustCode(t, "move "+ag.Name, c, ec, 409, code)
	}

	// A template of the old team's own (not in the catalog).
	var tpl apitypes.AgentTemplate
	code, ec := e.user.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "tools-only", "name": "Tools only", "instructions": "Hi."},
		&tpl, nil)
	mustCode(t, "team template", code, ec, 201, "")
	refused(e.agent(toolsTeam, "On a team template", map[string]any{"templateId": tpl.Id}), "dest", "move_template_unavailable", nil)

	// A model published to the old team only; moving with a model the new team may use works.
	adminActor := authz.Actor{UserID: e.admin.me.User.Id, PlatformRole: authz.PlatformAdmin}
	prov, err := e.app.Models.CreateProvider(ctx, adminActor, llm.ProviderInput{Name: "upstream", Kind: "openai-compatible",
		BaseURL: "http://127.0.0.1:9/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	toolsOnly, err := e.app.Models.CreateModel(ctx, adminActor, prov.Provider.ID, llm.ModelInput{UpstreamID: "tools-model", Visibility: "teams", Teams: []string{"tools"}})
	if err != nil {
		t.Fatal(err)
	}
	everyone, err := e.app.Models.CreateModel(ctx, adminActor, prov.Provider.ID, llm.ModelInput{UpstreamID: "shared-model"})
	if err != nil {
		t.Fatal(err)
	}
	modeled := e.agent(toolsTeam, "On a team's model", map[string]any{"modelId": toolsOnly.Model.ID})
	refused(modeled, "dest", "move_model_unavailable", nil)
	p, code, ec := e.plan(e.user, modeled.Id, "dest")
	mustCode(t, "plan", code, ec, 200, "")
	if p.Model == nil || p.Model.Id != toolsOnly.Model.ID || p.Model.Available {
		t.Fatalf("the model there: %+v", p.Model)
	}
	var switched apitypes.AgentMovePlan
	code, ec = e.user.call("GET", "/v1/agents/"+modeled.Id.String()+"/move?team=dest&modelId="+everyone.Model.ID.String(), nil, &switched, nil)
	if code != 200 || !switched.Allowed || switched.Model == nil || switched.Model.Id != everyone.Model.ID || !switched.Model.Available {
		t.Fatalf("plan with another model: %d %s %+v", code, ec, switched)
	}
	var moved apitypes.Agent
	code, ec = e.user.call("POST", "/v1/agents/"+modeled.Id.String()+"/move", map[string]any{"team": "dest", "modelId": everyone.Model.ID},
		&moved, ifMatch(modeled.Revision))
	mustCode(t, "move with another model", code, ec, 200, "")
	if moved.Model == nil || moved.Model.Id != everyone.Model.ID || moved.TeamSlug != "dest" {
		t.Fatalf("moved %+v", moved)
	}

	// A name the new team uses.
	e.agent(destTeam, "Twin", nil)
	refused(e.agent(toolsTeam, "twin", nil), "dest", "name_taken", nil)

	// A service-managed agent (an app's users' conversations) stays.
	managed := send(t, e.app, e.chat, "PUT", toolsTeam+"/agents/by-key/user-7", nil, nil)
	if managed.code != 201 {
		t.Fatalf("managed: %+v", managed)
	}
	refused(managed.agent, "dest", "agent_service_managed", nil)
}

// Personal workspaces: their owner moves agents in and out (dropping a
// key, which named the agent in the workspace); platform admins don't.
func TestAgentMovePersonalWorkspace(t *testing.T) {
	e := newMoveEnv(t)
	ag := e.agent(toolsTeam, "Mine now", nil)
	code, ec := e.user.call("POST", "/v1/agents/"+ag.Id.String()+"/move", map[string]any{"team": personalSlug(t, e.user)}, nil, ifMatch(ag.Revision))
	mustCode(t, "into the owner's workspace", code, ec, 200, "")
	_, code, ec = e.plan(e.admin, ag.Id, "tools")
	mustCode(t, "a platform admin moves it out of someone's workspace", code, ec, 403, "forbidden")
	_, code, ec = e.plan(e.alex, ag.Id, "dest")
	mustCode(t, "someone else plans it", code, ec, 404, "agent_not_found")
	other := e.agent(destTeam, "Not alex's", nil)
	_, code, ec = e.plan(e.alex, other.Id, personalSlug(t, e.user))
	mustCode(t, "into someone else's workspace", code, ec, 404, "team_not_found")
	_, code, ec = e.plan(e.admin, other.Id, personalSlug(t, e.user))
	mustCode(t, "a platform admin moves into someone's workspace", code, ec, 403, "forbidden")

	// A keyed agent of the workspace: its key is dropped on the way out.
	keyed := send(t, e.app, viaSession(e.user), "PUT", e.userWS+"/agents/by-key/notes", map[string]any{"name": "Notes"}, nil)
	if keyed.code != 201 {
		t.Fatalf("keyed: %+v", keyed)
	}
	p, code, ec := e.plan(e.user, keyed.agent.Id, "dest")
	if code != 200 || !p.Allowed || p.ExternalKey == nil || *p.ExternalKey != "notes" || p.From.Kind != "personal" {
		t.Fatalf("plan %d %s %+v", code, ec, p)
	}
	var moved apitypes.Agent
	code, ec = e.user.call("POST", "/v1/agents/"+keyed.agent.Id.String()+"/move", map[string]any{"team": "dest"}, &moved, ifMatch(keyed.agent.Revision))
	mustCode(t, "out of the workspace", code, ec, 200, "")
	if moved.ExternalKey != nil || moved.ServiceManaged || moved.TeamSlug != "dest" {
		t.Fatalf("moved %+v", moved)
	}
	if code := e.casey.get("/v1/agents/"+moved.Id.String(), nil); code != 200 {
		t.Fatalf("the new team's member: %d", code)
	}
	if again := send(t, e.app, viaSession(e.user), "PUT", e.userWS+"/agents/by-key/notes", nil, nil); again.code != 201 || again.agent.Id == moved.Id {
		t.Fatalf("the key names a new agent: %+v", again)
	}
}
