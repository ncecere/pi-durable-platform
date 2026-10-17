package httpapi

import (
	"net/http"
	"regexp"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
)

// Agents (api/openapi.yaml, tag agents). internal/agents authorizes every
// call. Conversation routes are relayed to the agent's runtime (waking it
// first) by internal/agenthost; the runtime answers in the same envelope.

func (a *api) agentRoutes() []route {
	const one = "/v1/agents/{agentId}"
	const thread = one + "/threads/{threadId}"
	// Every agent route takes a browser session or a key (sessionOrKey).
	k := a.sessionOrKey
	return []route{
		{"GET", "/v1/agents", k(a.listAgents)},
		{"GET", "/v1/teams/{team}/agents", k(a.listTeamAgents)},
		{"POST", "/v1/teams/{team}/agents", k(a.createTeamAgent)},
		{"PUT", "/v1/teams/{team}/agents/by-key/{externalKey}", k(a.ensureTeamAgentByKey)},
		{"GET", one, k(a.getAgent)},
		{"PATCH", one, k(a.updateAgent)},
		{"DELETE", one, k(a.deleteAgent)},
		{"POST", one + "/hibernate", k(a.hibernateAgent)},
		{"GET", one + "/threads", k(a.agentProxy("/threads"))},
		{"GET", thread, k(a.agentProxy(""))},
		{"DELETE", thread, k(a.agentProxy(""))},
		{"POST", thread + "/messages", k(a.agentProxy("/messages"))},
		{"POST", thread + "/stop", k(a.agentProxy("/stop"))},
		{"GET", thread + "/events", k(a.agentProxy("/events"))},
	}
}

func toAPIAgent(v agents.View) apitypes.Agent {
	ag := v.Agent
	return apitypes.Agent{
		Id: ag.ID, Name: ag.Name, Description: ag.Description,
		TeamId: ag.TeamID, TeamSlug: v.TeamSlug, TeamName: v.TeamName, TemplateId: ag.TemplateID, TemplateSlug: v.TemplateSlug,
		TemplateName: v.TemplateName, TemplateVersion: ag.TemplateVersion, TemplateCurrentVersion: v.TemplateCurrentVersion,
		Enabled: ag.Enabled, Running: v.Running, LastUsedAt: ag.LastUsedAt, Revision: ag.Revision,
		CreatedAt: ag.CreatedAt, UpdatedAt: ag.UpdatedAt, ExternalKey: ag.ExternalKey,
	}
}

func (a *api) writeAgent(w http.ResponseWriter, r *http.Request, status int, v agents.View, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Agent.Revision, toAPIAgent(v))
}

// listAgents reads ?includeManaged=true.
func (a *api) listAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.List(r.Context(), a.actor(r), r.URL.Query().Get("includeManaged") == "true")
	writeList(w, r, rows, err, toAPIAgent)
}

func (a *api) createTeamAgent(w http.ResponseWriter, r *http.Request) {
	var in apitypes.AgentCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	ci := agents.CreateInput{Name: in.Name, TemplateID: in.TemplateId}
	setIf(&ci.Description, in.Description)
	setIf(&ci.Template, in.Template)
	v, err := a.Agents.CreateTeamAgent(r.Context(), a.actor(r), r.PathValue("team"), ci)
	a.writeAgent(w, r, http.StatusCreated, v, err)
}

// listTeamAgents reads ?includeManaged=true (service accounts: also the
// team's service-managed agents).
func (a *api) listTeamAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.ListTeam(r.Context(), a.actor(r), r.PathValue("team"), r.URL.Query().Get("includeManaged") == "true")
	writeList(w, r, rows, err, toAPIAgent)
}

// ensureTeamAgentByKey returns the team's service-managed agent with the
// key, creating it the first time (201).
func (a *api) ensureTeamAgentByKey(w http.ResponseWriter, r *http.Request) {
	var in apitypes.AgentEnsure
	if r.ContentLength != 0 && !httpx.Decode(w, r, &in) {
		return
	}
	ci := agents.CreateInput{TemplateID: in.TemplateId}
	setIf(&ci.Name, in.Name)
	setIf(&ci.Description, in.Description)
	setIf(&ci.Template, in.Template)
	v, created, err := a.Agents.EnsureByKey(r.Context(), a.actor(r), r.PathValue("team"), r.PathValue("externalKey"), ci)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	a.writeAgent(w, r, status, v, err)
}

func (a *api) getAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	v, err := a.Agents.Get(r.Context(), a.actor(r), id)
	a.writeAgent(w, r, http.StatusOK, v, err)
}

func (a *api) updateAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	in, rev, ok := decodeRevised[apitypes.AgentUpdate](w, r)
	if !ok {
		return
	}
	v, err := a.Agents.Update(r.Context(), a.actor(r), id, agents.UpdateInput{Name: in.Name, Description: in.Description, Enabled: in.Enabled, TemplateVersion: in.TemplateVersion}, rev)
	a.writeAgent(w, r, http.StatusOK, v, err)
}

func (a *api) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	writeOK(w, r, a.Agents.Delete(r.Context(), a.actor(r), id))
}

func (a *api) hibernateAgent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	writeOK(w, r, a.Agents.Hibernate(r.Context(), a.actor(r), id))
}

var threadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// agentProxy relays to the runtime path /threads[/{threadId}{suffix}] after
// authorizing the caller for the agent. The request body is limited like any
// other /v1 write.
func (a *api) agentProxy(suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathUUID(w, r, "agentId")
		if !ok {
			return
		}
		path := "/threads"
		if suffix != "/threads" {
			threadID := r.PathValue("threadId")
			if !threadIDRE.MatchString(threadID) {
				httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
				return
			}
			path += "/" + threadID + suffix
		}
		spec, err := a.Agents.Spec(r.Context(), a.actor(r), id)
		if failed(w, r, err) {
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		}
		a.Agents.Host().Proxy(w, r, spec, path)
	}
}
