package httpapi_test

import "fmt"

// The authorization matrix's classification of every operation in
// api/openapi.yaml: who may call it on their own team (team
// B) or at all (global routes), and who may call it on another team (team
// A). Platform admins and auditors read any team's metadata; only platform
// admins change platform objects; team owners and admins manage members;
// editors and above read the team's audit log.

// caller is a kind of credential.
type caller int

const (
	cAnon caller = iota
	cMember
	cEditor
	cTeamAdmin
	cOwner
	cPlatformAdmin
	cAuditor
	numCallers
)

var callerNames = [numCallers]string{"anonymous", "member", "editor", "team-admin", "owner", "platform-admin", "auditor"}

func (c caller) String() string { return callerNames[c] }

// callers is a set of callers.
type callers uint32

func of(cs ...caller) callers {
	var s callers
	for _, c := range cs {
		s |= 1 << c
	}
	return s
}

func (s callers) has(c caller) bool { return s&(1<<c) != 0 }

var (
	everyone = callers(1<<numCallers - 1)
	members  = of(cMember, cEditor, cTeamAdmin, cOwner)
	editors  = of(cEditor, cTeamAdmin, cOwner)
	admins   = of(cTeamAdmin, cOwner)
	platform = of(cPlatformAdmin, cAuditor)
	padmin   = of(cPlatformAdmin)
	signedIn = members | platform
)

// scope says how an operation is exercised.
type scope int

const (
	// scopeGlobal: no team in the path; every caller calls it once.
	scopeGlobal scope = iota
	// scopeTeam: the path names a team; team B's callers call it on team B,
	// and every caller on team A.
	scopeTeam
)

// policy is one operation's classification.
type policy struct {
	scope scope
	// own: callers allowed on their own team (or at all, for scopeGlobal).
	own callers
	// foreign: callers allowed on team A.
	foreign callers
	// anyStatus: the status is not checked (sign-in plumbing), only that the
	// call didn't fail on the server.
	anyStatus bool
	build     func(c *mctx) request
}

// request is one call.
type request struct {
	method, path string
	body         any
	headers      map[string]string
	// anon: send without the caller's session (sign-in endpoints would
	// replace it).
	anon bool
	// ownSession: call with a new session of the caller's user (logout
	// would end the shared one).
	ownSession bool
}

func get(p string) request                       { return request{method: "GET", path: p} }
func post(p string, body any) request            { return request{method: "POST", path: p, body: body} }
func patch(p string, body any) request           { return request{method: "PATCH", path: p, body: body} }
func del(p string) request                       { return request{method: "DELETE", path: p} }
func (r request) h(hs map[string]string) request { r.headers = hs; return r }

var rev1 = map[string]string{"If-Match": `"1"`}

var matrixPolicies = map[string]policy{
	// Operations and sign-in.
	"getHealthz":    {own: everyone, build: func(*mctx) request { return get("/healthz") }},
	"getReadyz":     {own: everyone, build: func(*mctx) request { return get("/readyz") }},
	"getAuthConfig": {own: everyone, build: func(*mctx) request { return get("/v1/auth/config") }},
	"startLogin":    {anyStatus: true, build: func(*mctx) request { r := get("/auth/login"); r.anon = true; return r }},
	"completeLogin": {anyStatus: true, build: func(*mctx) request { r := get("/auth/callback?state=x"); r.anon = true; return r }},
	"devLogin": {anyStatus: true, build: func(*mctx) request {
		r := post("/auth/dev", map[string]string{"account": "casey"})
		r.anon = true
		return r
	}},
	"logout":      {own: signedIn, build: func(*mctx) request { r := post("/auth/logout", nil); r.ownSession = true; return r }},
	"getMe":       {own: signedIn, build: func(*mctx) request { return get("/v1/me") }},
	"listMyTeams": {own: signedIn, build: func(*mctx) request { return get("/v1/teams") }},

	// Teams, members, invites and the team audit log.
	"getTeam":     {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("")) }},
	"listMembers": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/members")) }},
	"addMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/members"), map[string]any{"email": fmt.Sprintf("added-%d@example.com", c.e.next()), "role": "member"})
	}},
	"updateMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/members/"+c.member()), map[string]any{"role": "editor"}).h(rev1)
	}},
	"removeMember": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return del(c.team("/members/" + c.member())) }},
	"listInvites":  {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request { return get(c.team("/invites")) }},
	"revokeInvite": {scope: scopeTeam, own: admins, foreign: padmin, build: func(c *mctx) request {
		return del(c.team("/invites/" + c.invite()))
	}},
	"listTeamAudit": {scope: scopeTeam, own: editors, foreign: platform, build: func(c *mctx) request { return get(c.team("/audit")) }},
	"getTeamAuditEntry": {scope: scopeTeam, own: editors, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/audit/" + c.tf.auditID))
	}},

	// Platform administration.
	"adminListUsers": {own: platform, build: func(*mctx) request { return get("/v1/admin/users") }},
	"adminGetUser":   {own: platform, build: func(c *mctx) request { return get("/v1/admin/users/" + c.e.spareUser) }},
	"adminUpdateUser": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/users/"+c.user(), map[string]any{"platformRole": "platform_auditor"}).h(rev1)
	}},
	"adminListTeams": {own: platform, build: func(*mctx) request { return get("/v1/admin/teams") }},
	"adminCreateTeam": {own: padmin, build: func(c *mctx) request {
		n := c.e.next()
		return post("/v1/admin/teams", map[string]any{"slug": fmt.Sprintf("new-%d", n), "name": "New", "ownerEmail": fmt.Sprintf("o-%d@example.com", n)})
	}},
	"adminGetTeam": {own: platform, build: func(c *mctx) request { return get("/v1/admin/teams/" + c.e.b.slug) }},
	"adminUpdateTeam": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/teams/"+c.spareTeam(), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"adminAssignOwner": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/teams/"+c.spareTeam()+"/owners", map[string]any{"email": fmt.Sprintf("owner-%d@example.com", c.e.next())})
	}},
	"adminListAudit":     {own: platform, build: func(*mctx) request { return get("/v1/admin/audit") }},
	"adminGetAuditEntry": {own: platform, build: func(c *mctx) request { return get("/v1/admin/audit/" + c.e.b.auditID) }},

	// The caller's API keys.
	"listMyApiKeys": {own: signedIn, build: func(*mctx) request { return get("/v1/me/api-keys") }},
	"createMyApiKey": {own: signedIn, build: func(*mctx) request {
		return post("/v1/me/api-keys", map[string]any{"name": "matrix key"})
	}},
	"revokeMyApiKey": {own: signedIn, build: func(c *mctx) request { return del("/v1/me/api-keys/" + c.apiKey()) }},

	// Service accounts (owners and admins manage them and their keys;
	// members and platform staff see them; platform staff see keys and
	// every team's accounts).
	"listServiceAccounts": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/service-accounts")) }},
	"createServiceAccount": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/service-accounts"), map[string]any{"name": fmt.Sprintf("bot-%d", c.e.next()), "role": "editor"})
	}},
	"getServiceAccount": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/service-accounts/" + c.tf.serviceAccount))
	}},
	"updateServiceAccount": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/service-accounts/"+c.serviceAccount()), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"deleteServiceAccount": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return del(c.team("/service-accounts/" + c.serviceAccount()))
	}},
	"adminListServiceAccounts": {own: platform, build: func(*mctx) request { return get("/v1/admin/service-accounts?q=sa-&limit=5") }},
	"listServiceAccountKeys": {scope: scopeTeam, own: admins, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/service-accounts/" + c.tf.serviceAccount + "/keys"))
	}},
	"createServiceAccountKey": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/service-accounts/"+c.serviceAccount()+"/keys"), map[string]any{"name": "matrix key", "expiresInDays": 30})
	}},
	"revokeServiceAccountKey": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		sa, key := c.serviceAccountKey()
		return del(c.team("/service-accounts/" + sa + "/keys/" + key))
	}},

	// Agents. Everyone signed in sees the templates and their teams' agents.
	// A team's agents are its members' to use, their conversations its
	// members' only: platform staff see any team's agents read-only (their
	// settings and status), and platform admins may delete them.
	"listAgentTemplates": {own: signedIn, build: func(*mctx) request { return get("/v1/agent-templates") }},

	// Templates. Platform templates are platform admins'; a team's templates
	// are private to it (platform staff read them, to review); its owners
	// and admins ask to publish, platform admins review.
	"createPlatformTemplate": {own: padmin, build: func(c *mctx) request {
		return post("/v1/agent-templates", map[string]any{"slug": fmt.Sprintf("p-%d", c.e.next()), "name": "P", "instructions": "Hi."})
	}},
	"adminListTemplates": {own: platform, build: func(*mctx) request { return get("/v1/admin/agent-templates?status=pending") }},
	"listTeamTemplates":  {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/agent-templates")) }},
	"createTeamTemplate": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.team("/agent-templates"), map[string]any{"slug": fmt.Sprintf("t-%d", c.e.next()), "name": "T", "instructions": "Hi."})
	}},
	"getAgentTemplate": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.template("", false, "")) }},
	"updateAgentTemplate": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return patch(c.template("", true, "private"), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"listTemplateVersions": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.template("/versions", false, ""))
	}},
	"createTemplateVersion": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.template("/versions", true, "private"), map[string]any{"instructions": "Be brief."})
	}},
	"publishTemplate":   {scope: scopeTeam, own: admins, build: func(c *mctx) request { return post(c.template("/publish", true, "private"), nil) }},
	"unpublishTemplate": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return post(c.template("/unpublish", true, "published"), nil) }},
	"reviewTemplate": {scope: scopeTeam, own: 0, foreign: padmin, build: func(c *mctx) request {
		return post(c.template("/review", true, "pending"), map[string]any{"decision": "approve"})
	}},

	"listAgents": {own: signedIn, build: func(*mctx) request { return get("/v1/agents") }},
	"createTeamAgent": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return post(c.team("/agents"), map[string]any{"name": fmt.Sprintf("new-agent-%d", c.e.next())})
	}},
	"listTeamAgents": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/agents")) }},
	// Service-managed agents are created by editor service accounts only (agents_keys_integration_test.go).
	"ensureTeamAgentByKey": {scope: scopeTeam, own: 0, build: func(c *mctx) request {
		return request{method: "PUT", path: c.team(fmt.Sprintf("/agents/by-key/user-%d", c.e.next()))}
	}},
	"getAgent": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.agent("", false)) }},
	"updateAgent": {scope: scopeTeam, own: editors, build: func(c *mctx) request {
		return patch(c.agent("", true), map[string]any{"description": "updated by the matrix"}).h(rev1)
	}},
	"deleteAgent":      {scope: scopeTeam, own: admins | padmin, foreign: padmin, build: func(c *mctx) request { return del(c.agent("", true)) }},
	"hibernateAgent":   {scope: scopeTeam, own: members, build: func(c *mctx) request { return post(c.agent("/hibernate", true), nil) }},
	"listAgentThreads": {scope: scopeTeam, own: members, build: func(c *mctx) request { return get(c.agent("/threads", false)) }},
	"getAgentThread":   {scope: scopeTeam, own: members, build: func(c *mctx) request { return get(c.thread("", false)) }},
	"updateAgentThread": {scope: scopeTeam, own: members, build: func(c *mctx) request {
		return patch(c.thread("", false), map[string]any{"title": "Renamed by the matrix"})
	}},
	"streamAgentEvents": {own: signedIn, build: func(*mctx) request { return get("/v1/agents/events?once=true") }},
	"deleteAgentThread": {scope: scopeTeam, own: members, build: func(c *mctx) request { return del(c.thread("", true)) }},
	"sendAgentMessage": {scope: scopeTeam, own: members, build: func(c *mctx) request {
		return post(c.thread("/messages", false), map[string]any{"text": "hello from the matrix"})
	}},
	"stopAgentThread":   {scope: scopeTeam, own: members, build: func(c *mctx) request { return post(c.thread("/stop", false), nil) }},
	"streamAgentThread": {scope: scopeTeam, own: members, build: func(c *mctx) request { return get(c.thread("/events?once=true", false)) }},
	// Platform admins (not auditors) list every thread of any agent, and read
	// any conversation with a reason (audited in the agent's team's log).
	"adminListAgentThreads": {scope: scopeTeam, foreign: padmin, build: func(c *mctx) request {
		return get("/v1/admin/agents/" + c.tf.agent + "/threads")
	}},
	"adminReadAgentThread": {scope: scopeTeam, foreign: padmin, build: func(c *mctx) request {
		return post(c.adminThread("/read"), map[string]any{"reason": "Checking a reported problem"})
	}},

	// The model catalog: platform admins manage providers and models,
	// auditors read them; a team's members (and platform staff) see the
	// models it may use.
	"adminListModelProviderKinds": {own: platform, build: func(*mctx) request { return get("/v1/admin/model-provider-kinds") }},
	"adminListModelProviders":     {own: platform, build: func(*mctx) request { return get("/v1/admin/model-providers") }},
	"adminCreateModelProvider": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/model-providers", map[string]any{"name": fmt.Sprintf("p-%d", c.e.next()), "kind": "openai-compatible",
			"baseUrl": c.e.upstream + "/v1", "apiKey": "matrix-key"})
	}},
	"adminGetModelProvider": {own: platform, build: func(c *mctx) request { return get("/v1/admin/model-providers/" + c.provider(false)) }},
	"adminUpdateModelProvider": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/model-providers/"+c.provider(true), map[string]any{"enabled": false}).h(rev1)
	}},
	"adminDeleteModelProvider": {own: padmin, build: func(c *mctx) request { return del("/v1/admin/model-providers/" + c.provider(true)) }},
	"adminDiscoverModels": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/model-providers/"+c.provider(false)+"/discover", nil)
	}},
	"adminListProviderModels": {own: platform, build: func(c *mctx) request {
		return get("/v1/admin/model-providers/" + c.provider(false) + "/models")
	}},
	"adminCreateModel": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/model-providers/"+c.provider(false)+"/models", map[string]any{"upstreamId": fmt.Sprintf("m-%d", c.e.next())})
	}},
	"adminListModels": {own: platform, build: func(*mctx) request { return get("/v1/admin/models") }},
	"adminUpdateModel": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/models/"+c.model(true), map[string]any{"enabled": false}).h(rev1)
	}},
	"adminDeleteModel":      {own: padmin, build: func(c *mctx) request { return del("/v1/admin/models/" + c.model(true)) }},
	"listTeamModels":        {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/models")) }},
	"adminGetModelSettings": {own: platform, build: func(*mctx) request { return get("/v1/admin/settings/models") }},
	"adminUpdateModelSettings": {own: padmin, build: func(*mctx) request {
		return request{method: "PUT", path: "/v1/admin/settings/models",
			body: map[string]any{"teamsMayAddProviders": true, "privateNetworks": []string{"10.0.0.0/8"}}}.h(rev1)
	}},

	// A team's own model providers and models: its owners and admins manage
	// them (browser sessions only), its members and platform staff read
	// them; platform admins manage them through the admin routes.
	"listTeamModelProviderKinds": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/model-provider-kinds"))
	}},
	"listTeamModelProviders": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/model-providers"))
	}},
	"createTeamModelProvider": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/model-providers"), map[string]any{"name": fmt.Sprintf("tp-%d", c.e.next()), "kind": "openai-compatible",
			"baseUrl": matrixTeamProviderURL, "apiKey": "matrix-key"})
	}},
	"getTeamModelProvider": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/model-providers/" + c.tf.provider))
	}},
	"updateTeamModelProvider": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/model-providers/"+c.teamProvider(true)), map[string]any{"enabled": false}).h(rev1)
	}},
	"deleteTeamModelProvider": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return del(c.team("/model-providers/" + c.teamProvider(true)))
	}},
	"discoverTeamModels": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/model-providers/"+c.tf.provider+"/discover"), nil)
	}},
	"listTeamProviderModels": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/model-providers/" + c.tf.provider + "/models"))
	}},
	"createTeamModel": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/model-providers/"+c.tf.provider+"/models"), map[string]any{"upstreamId": fmt.Sprintf("tm-%d", c.e.next())})
	}},
	"updateTeamModel": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/models/"+c.teamModel(true)), map[string]any{"enabled": false}).h(rev1)
	}},
	"deleteTeamModel": {scope: scopeTeam, own: admins, build: func(c *mctx) request { return del(c.team("/models/" + c.teamModel(true))) }},
	"getTeamUsage":    {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/usage?groupBy=agent")) }},
	"adminGetUsage":   {own: platform, build: func(*mctx) request { return get("/v1/admin/usage?groupBy=team") }},

	// Personal workspaces: platform admins set the defaults and override one
	// workspace's limits; auditors read the defaults; a workspace's members
	// and platform staff read its limits.
	"adminGetPersonalSettings": {own: platform, build: func(*mctx) request { return get("/v1/admin/settings/personal-workspaces") }},
	"adminUpdatePersonalSettings": {own: padmin, build: func(*mctx) request {
		return request{method: "PUT", path: "/v1/admin/settings/personal-workspaces",
			body: map[string]any{"enabled": false, "maxAgents": 5, "maxRunning": 2}}.h(rev1)
	}},
	"adminSetWorkspaceLimits": {own: padmin, build: func(c *mctx) request {
		return request{method: "PUT", path: "/v1/admin/teams/" + c.personalTeam() + "/limits", body: map[string]any{"maxAgents": 3}}.h(rev1)
	}},
	"getWorkspaceLimits": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request { return get(c.team("/limits")) }},

	// The MCP catalog: platform admins manage servers, auditors read them; a
	// team's members (and platform staff) see what the team may use; anyone
	// who may use an agent sets the MCP servers an app gives its thread.
	"adminListMcpServers": {own: platform, build: func(*mctx) request { return get("/v1/admin/mcp-servers") }},
	"adminCreateMcpServer": {own: padmin, build: func(c *mctx) request {
		return post("/v1/admin/mcp-servers", map[string]any{"name": fmt.Sprintf("mcp-%d", c.e.next()), "url": c.e.upstream + "/mcp"})
	}},
	"adminGetMcpServer": {own: platform, build: func(c *mctx) request { return get("/v1/admin/mcp-servers/" + c.mcpServer(false)) }},
	"adminUpdateMcpServer": {own: padmin, build: func(c *mctx) request {
		return patch("/v1/admin/mcp-servers/"+c.mcpServer(true), map[string]any{"enabled": false}).h(rev1)
	}},
	"adminDeleteMcpServer":  {own: padmin, build: func(c *mctx) request { return del("/v1/admin/mcp-servers/" + c.mcpServer(true)) }},
	"adminRefreshMcpServer": {own: padmin, build: func(c *mctx) request { return post("/v1/admin/mcp-servers/"+c.mcpServer(false)+"/refresh", nil) }},
	"listTeamMcpServers": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers"))
	}},

	// A team's own MCP servers: its owners and admins manage them (browser
	// sessions only), its members and platform staff read them; platform
	// admins manage them through the admin routes.
	"listTeamOwnMcpServers": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers/own"))
	}},
	"createTeamMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/own"), map[string]any{"name": fmt.Sprintf("tmcp-%d", c.e.next()), "url": matrixTeamMCPURL})
	}},
	"getTeamOwnMcpServer": {scope: scopeTeam, own: members, foreign: platform, build: func(c *mctx) request {
		return get(c.team("/mcp-servers/own/" + c.tf.mcpServer))
	}},
	"updateTeamOwnMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return patch(c.team("/mcp-servers/own/"+c.teamMCPServer(true)), map[string]any{"enabled": false}).h(rev1)
	}},
	"deleteTeamOwnMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return del(c.team("/mcp-servers/own/" + c.teamMCPServer(true)))
	}},
	"refreshTeamOwnMcpServer": {scope: scopeTeam, own: admins, build: func(c *mctx) request {
		return post(c.team("/mcp-servers/own/"+c.tf.mcpServer+"/refresh"), nil)
	}},
	"getThreadMcpServers": {scope: scopeTeam, own: members, build: func(c *mctx) request { return get(c.thread("/mcp-servers", false)) }},
	"setThreadMcpServers": {scope: scopeTeam, own: members, build: func(c *mctx) request {
		return request{method: "PUT", path: c.thread("/mcp-servers", false), body: map[string]any{"servers": []any{}}}
	}},

	// OAuth Connect: everyone signed in sees an app's request and decides
	// it, and an app's logo (public by nature), and lists and disconnects their own apps; platform staff read
	// anyone's apps and the trusted list; only platform admins disconnect
	// another person's apps or change the list.
	"getOAuthConsent": {own: signedIn, build: func(c *mctx) request { return get("/v1/oauth/consent?" + c.e.consentQuery()) }},
	"decideOAuthConsent": {own: signedIn, build: func(c *mctx) request {
		return post("/v1/oauth/consent", map[string]any{"query": c.e.consentQuery(), "decision": "deny"})
	}},
	"getOAuthClientLogo": {own: signedIn, build: func(c *mctx) request {
		return get("/v1/oauth/client-logo?client_id=" + c.e.oauthClient)
	}},
	"listMyOAuthGrants":  {own: signedIn, build: func(*mctx) request { return get("/v1/me/oauth-grants") }},
	"revokeMyOAuthGrant": {own: signedIn, build: func(c *mctx) request { return del("/v1/me/oauth-grants/" + c.myGrant()) }},
	"adminListUserOAuthGrants": {own: platform, build: func(c *mctx) request {
		return get("/v1/admin/users/" + c.e.spareUser + "/oauth-grants")
	}},
	"adminRevokeUserOAuthGrant": {own: padmin, build: func(c *mctx) request {
		return del("/v1/admin/users/" + c.e.spareUser + "/oauth-grants/" + c.spareGrant())
	}},
	"adminGetTrustedClients": {own: platform, build: func(*mctx) request { return get("/v1/admin/settings/oauth-clients") }},
	"adminUpdateTrustedClients": {own: padmin, build: func(*mctx) request {
		return request{method: "PUT", path: "/v1/admin/settings/oauth-clients",
			body: map[string]any{"clients": []any{map[string]any{"clientId": "https://chat.example.com/client.json"}}}}.h(rev1)
	}},
}
