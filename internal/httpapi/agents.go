package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"

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
		{"GET", "/v1/agents/events", k(a.streamAgentEvents)},
		{"GET", one + "/threads", k(a.listAgentThreads)},
		{"GET", thread, k(a.agentProxy(""))},
		{"PATCH", thread, k(a.updateAgentThread)},
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
		Model: agentModel(v.Model),
	}
}

func agentModel(m *agents.ModelRef) *apitypes.AgentModel {
	if m == nil {
		return nil
	}
	return toAPIAgentModel(&m.ID, m.Name, m.ProviderName, m.Source, m.Available)
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
	ci := agents.CreateInput{Name: in.Name, TemplateID: in.TemplateId, ModelID: in.ModelId}
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
	ci := agents.CreateInput{TemplateID: in.TemplateId, ModelID: in.ModelId}
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
	v, err := a.Agents.Update(r.Context(), a.actor(r), id, agents.UpdateInput{Name: in.Name, Description: in.Description, Enabled: in.Enabled, TemplateVersion: in.TemplateVersion, ModelID: in.ModelId}, rev)
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
		received := time.Now()
		var text string
		if suffix == "/messages" {
			if text, ok = a.takeMCPServers(w, r, r.PathValue("threadId")); !ok {
				return
			}
		}
		rec := &statusRecorder{ResponseWriter: w}
		a.Agents.Host().Proxy(rec, r, spec, path)
		// Keep the platform's record of the thread (agent_threads) in step.
		if rec.status >= 200 && rec.status < 300 {
			switch {
			case suffix == "/messages":
				a.Agents.ThreadSent(r.Context(), a.actor(r), id, r.PathValue("threadId"), text, received)
			case suffix == "" && r.Method == http.MethodDelete:
				a.Agents.ThreadRemoved(r.Context(), id, r.PathValue("threadId"))
			}
		}
	}
}

func toAPIThread(t agents.Thread) apitypes.AgentThread {
	out := apitypes.AgentThread{ThreadId: t.ThreadID, Title: t.Title, CreatedAt: t.CreatedAt.UnixMilli(), UpdatedAt: t.UpdatedAt.UnixMilli(),
		Busy: t.Busy, LastError: t.LastError}
	if t.LastReplyAt != nil {
		ms := t.LastReplyAt.UnixMilli()
		out.LastReplyAt = &ms
	}
	return out
}

// listAgentThreads reads the platform's record: it doesn't wake the agent.
func (a *api) listAgentThreads(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	rows, err := a.Agents.Threads(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, toAPIThread)
}

func (a *api) updateAgentThread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	thread := r.PathValue("threadId")
	if !threadIDRE.MatchString(thread) {
		httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
		return
	}
	var in apitypes.AgentThreadUpdate
	if !httpx.Decode(w, r, &in) {
		return
	}
	t, err := a.Agents.RenameThread(r.Context(), a.actor(r), id, thread, in.Title)
	if failed(w, r, err) {
		return
	}
	httpx.JSON(w, http.StatusOK, toAPIThread(t))
}

// streamAgentEvents sends a "thread" event whenever a thread of an agent the
// caller may use is created, renamed, starts or finishes a reply, or is
// deleted (?agentId= limits it to one agent; ?once=true answers "ready" and
// ends, for checks).
func (a *api) streamAgentEvents(w http.ResponseWriter, r *http.Request) {
	var only *uuid.UUID
	if v := r.URL.Query().Get("agentId"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_agent_id", "agentId must be an agent's ID")
			return
		}
		if _, err := a.Agents.Threads(r.Context(), a.actor(r), id); failed(w, r, err) {
			return
		}
		only = &id
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "internal", "Streaming is not supported")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := a.Agents.SubscribeThreads(ctx, a.actor(r), only)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	if r.URL.Query().Get("once") == "true" {
		return
	}
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	closing := a.Agents.Host().Closing()
	for {
		select {
		case <-ctx.Done():
			return
		case <-closing:
			return
		case <-keepAlive.C:
			_, _ = io.WriteString(w, ": keep-alive\n\n")
			flusher.Flush()
		case ev, open := <-events:
			if !open {
				return
			}
			body := apitypes.AgentThreadEvent{AgentId: ev.AgentID, ThreadId: ev.Thread.ThreadID, Deleted: ev.Deleted}
			if !ev.Deleted {
				t := toAPIThread(ev.Thread)
				body.Thread = &t
			}
			b, _ := json.Marshal(body)
			_, _ = io.WriteString(w, "event: thread\ndata: "+string(b)+"\n\n")
			flusher.Flush()
		}
	}
}
