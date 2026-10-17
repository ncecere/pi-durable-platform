package httpapi

import (
	"net/http"
	"regexp"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Agents (api/openapi.yaml, tag agents). internal/agents authorizes every
// call. Conversation routes are relayed to the agent's runtime (waking it
// first) by internal/agenthost; the runtime answers in the same envelope.

func (a *api) agentRoutes() []route {
	const one = "/v1/agents/{agentId}"
	const thread = one + "/threads/{threadId}"
	return []route{
		{"GET", "/v1/agent-templates", a.session(a.listAgentTemplates)},
		{"GET", "/v1/agents", a.session(a.listAgents)},
		{"POST", "/v1/me/assistant", a.session(a.ensureMyAssistant)},
		{"POST", "/v1/teams/{team}/agents", a.session(a.createTeamAgent)},
		{"GET", one, a.session(a.getAgent)},
		{"PATCH", one, a.session(a.updateAgent)},
		{"DELETE", one, a.session(a.deleteAgent)},
		{"POST", one + "/hibernate", a.session(a.hibernateAgent)},
		{"GET", one + "/threads", a.session(a.agentProxy("/threads"))},
		{"GET", thread, a.session(a.agentProxy(""))},
		{"DELETE", thread, a.session(a.agentProxy(""))},
		{"POST", thread + "/messages", a.session(a.agentProxy("/messages"))},
		{"POST", thread + "/stop", a.session(a.agentProxy("/stop"))},
		{"GET", thread + "/events", a.session(a.agentProxy("/events"))},
	}
}

func toAPIAgentTemplate(t dbgen.AgentTemplate) apitypes.AgentTemplate {
	return apitypes.AgentTemplate{Id: t.ID, Slug: t.Slug, Name: t.Name, Description: t.Description}
}

func toAPIAgent(v agents.View) apitypes.Agent {
	ag := v.Agent
	out := apitypes.Agent{
		Id: ag.ID, Kind: apitypes.AgentKind(ag.Kind), Name: ag.Name, Description: ag.Description,
		TeamSlug: v.TeamSlug, TeamName: v.TeamName, TemplateSlug: v.TemplateSlug, TemplateName: v.TemplateName,
		Enabled: ag.Enabled, Running: v.Running, LastUsedAt: ag.LastUsedAt, Revision: ag.Revision,
		CreatedAt: ag.CreatedAt, UpdatedAt: ag.UpdatedAt,
	}
	if ag.TeamID.Valid {
		id := ag.TeamID.UUID
		out.TeamId = &id
	}
	return out
}

func (a *api) writeAgent(w http.ResponseWriter, r *http.Request, status int, v agents.View, err error) {
	if failed(w, r, err) {
		return
	}
	writeRevised(w, status, v.Agent.Revision, toAPIAgent(v))
}

func (a *api) listAgentTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.Templates(r.Context())
	writeList(w, r, rows, err, toAPIAgentTemplate)
}

func (a *api) listAgents(w http.ResponseWriter, r *http.Request) {
	rows, err := a.Agents.List(r.Context(), a.actor(r))
	writeList(w, r, rows, err, toAPIAgent)
}

func (a *api) ensureMyAssistant(w http.ResponseWriter, r *http.Request) {
	v, created, err := a.Agents.Personal(r.Context(), a.actor(r))
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	a.writeAgent(w, r, status, v, err)
}

func (a *api) createTeamAgent(w http.ResponseWriter, r *http.Request) {
	var in apitypes.AgentCreate
	if !httpx.Decode(w, r, &in) {
		return
	}
	ci := agents.CreateInput{Name: in.Name}
	setIf(&ci.Description, in.Description)
	setIf(&ci.Template, in.Template)
	v, err := a.Agents.CreateTeamAgent(r.Context(), a.actor(r), r.PathValue("team"), ci)
	a.writeAgent(w, r, http.StatusCreated, v, err)
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
	v, err := a.Agents.Update(r.Context(), a.actor(r), id, agents.UpdateInput{Name: in.Name, Description: in.Description, Enabled: in.Enabled}, rev)
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
