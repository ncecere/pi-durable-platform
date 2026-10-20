package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
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
		{"POST", one + "/threads", k(a.createAgentThread)},
		{"GET", thread, k(a.agentProxy(""))},
		{"PATCH", thread, k(a.updateAgentThread)},
		{"DELETE", thread, k(a.agentProxy(""))},
		{"POST", thread + "/messages", k(a.agentProxy("/messages"))},
		{"POST", thread + "/stop", k(a.agentProxy("/stop"))},
		{"GET", thread + "/events", k(a.agentProxy("/events"))},
		// Platform admins: every thread, and reasoned reads (audited).
		{"GET", "/v1/admin/agents/{agentId}/threads", a.admin(a.adminListAgentThreads)},
		{"POST", "/v1/admin/agents/{agentId}/threads/{threadId}/read", a.admin(a.adminReadAgentThread)},
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
		var msg message
		if suffix == "/messages" {
			if msg, ok = a.takeMessage(w, r, id, r.PathValue("threadId"), received); !ok {
				return
			}
		} else if failed(w, r, a.Agents.ThreadAccess(r.Context(), a.actor(r), id, r.PathValue("threadId"))) {
			return
		}
		if suffix == "" && r.Method == http.MethodDelete {
			// A reserved thread has no conversation in the runtime yet: the platform deletes it
			// (and doesn't wake the agent for it).
			deleted, err := a.Agents.DeleteReservedThread(r.Context(), id, r.PathValue("threadId"))
			if failed(w, r, err) {
				return
			}
			if deleted {
				httpx.JSON(w, http.StatusOK, apitypes.Ok{Ok: true})
				return
			}
		}
		if suffix == "/events" && r.Method == http.MethodGet {
			// An open stream is cut when its person's access ends (denied, suspended,
			// removed from the team): access was decided once, above.
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			go a.Agents.WatchStreamAccess(ctx, a.actor(r), id, r.PathValue("threadId"), cancel)
			r = r.WithContext(ctx)
		}
		rec := &statusRecorder{ResponseWriter: w}
		a.Agents.Host().Proxy(rec, r, spec, path)
		// Keep the platform's record of the thread (agent_threads) in step.
		if rec.status >= 200 && rec.status < 300 {
			switch {
			case suffix == "/messages":
				a.Agents.ThreadSent(r.Context(), a.actor(r), msg.claim, msg.text, received)
			case suffix == "" && r.Method == http.MethodDelete:
				a.Agents.ThreadRemoved(r.Context(), id, r.PathValue("threadId"))
			}
		} else if suffix == "/messages" && unaccepted(rec.status) {
			a.Agents.Unclaim(r.Context(), msg.claim)
		}
	}
}

// message is a message being relayed: its text, and its thread's claim.
type message struct {
	text  string
	claim agents.Claim
}

// takeMessage prepares a message for the agent's runtime. It records the
// thread before relaying (ClaimThread: its creator and visibility, or the
// check that the caller may send to it) and stores the message's mcpServers
// for the thread. The body relayed has the fields only the platform decides:
// visibility and mcpServers are removed, any author the client sent is
// replaced by the caller, and announceAuthor says whether the agent tells its
// model who is speaking. It returns false when it has written an error.
func (a *api) takeMessage(w http.ResponseWriter, r *http.Request, agentID uuid.UUID, threadID string, at time.Time) (message, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "too_large", "The request is too large")
		return message{}, false
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || body == nil {
		httpx.Error(w, http.StatusBadRequest, "invalid_request", "The body must be a JSON object")
		return message{}, false
	}
	var msg message
	_ = json.Unmarshal(body["text"], &msg.text)
	if strings.TrimSpace(msg.text) == "" {
		httpx.Error(w, http.StatusBadRequest, "invalid_text", "text is required (at most 32,000 characters)")
		return message{}, false
	}
	var visibility string
	if v := body["visibility"]; v != nil && string(v) != "null" {
		if json.Unmarshal(v, &visibility) != nil || visibility == "" {
			httpx.Error(w, http.StatusBadRequest, "invalid_visibility", "visibility must be shared or private")
			return message{}, false
		}
	}
	var servers []apitypes.ClientMcpServer
	setServers := body["mcpServers"] != nil
	if setServers {
		if err := json.Unmarshal(body["mcpServers"], &servers); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid_mcp_servers", "mcpServers must be a list of {name, url, headers?, expiresInSeconds?}")
			return message{}, false
		}
	}
	actor := a.actor(r)
	if msg.claim, err = a.Agents.ClaimThread(r.Context(), actor, agentID, threadID, visibility, msg.text, at); failed(w, r, err) {
		return message{}, false
	}
	if setServers && failed(w, r, a.Agents.SetThreadMCP(r.Context(), actor, agentID, threadID, "", clientServers(servers))) {
		a.Agents.Unclaim(r.Context(), msg.claim)
		return message{}, false
	}
	for _, k := range []string{"visibility", "mcpServers", "author", "speaker", "announceAuthor"} {
		delete(body, k)
	}
	body["author"], _ = json.Marshal(msg.claim.Author)
	body["announceAuthor"], _ = json.Marshal(msg.claim.AnnounceAuthor)
	rest, _ := json.Marshal(body)
	r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(rest)), int64(len(rest))
	r.Header.Set("Content-Type", "application/json")
	return msg, true
}

// unaccepted reports whether a relayed message's status means the runtime
// surely did not start the thread: it refused the message (4xx), or the
// agent could not be started (503). Other failures keep a claimed record:
// the runtime reports or forgets the thread when it next starts.
func unaccepted(status int) bool {
	return (status >= 400 && status < 500) || status == http.StatusServiceUnavailable
}

func toAPIThread(t agents.ThreadView) apitypes.AgentThread {
	out := apitypes.AgentThread{ThreadId: t.ThreadID, Title: t.Title, CreatedAt: t.CreatedAt.UnixMilli(), UpdatedAt: t.UpdatedAt.UnixMilli(),
		Busy: t.Busy, LastError: t.LastError, Visibility: threadVisibility(t.Visibility), CreatedBy: toAPIPrincipal(t.CreatedBy),
		Participants: toAPIParticipants(t.Participants)}
	if t.LastReplyAt != nil {
		ms := t.LastReplyAt.UnixMilli()
		out.LastReplyAt = &ms
	}
	return out
}

func toAPIAdminThread(t agents.ThreadView) apitypes.AdminAgentThread {
	v := toAPIThread(t)
	out := apitypes.AdminAgentThread{ThreadId: v.ThreadId, TitleHidden: t.TitleHidden, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		Busy: v.Busy, LastReplyAt: v.LastReplyAt, LastError: v.LastError, Visibility: v.Visibility, CreatedBy: v.CreatedBy,
		Participants: v.Participants}
	if !t.TitleHidden {
		out.Title = &v.Title
	}
	return out
}

func threadVisibility(v string) apitypes.ThreadVisibility {
	if v == "" {
		return apitypes.ThreadVisibility(agents.VisibilityShared)
	}
	return apitypes.ThreadVisibility(v)
}

func toAPIPrincipal(p *agents.Principal) *apitypes.ThreadPrincipal {
	if p == nil {
		return nil
	}
	return &apitypes.ThreadPrincipal{Kind: apitypes.ThreadPrincipalKind(p.Kind), Id: p.ID, Name: p.Name}
}

func toAPIParticipants(ps []agents.Participant) []apitypes.ThreadParticipant {
	out := make([]apitypes.ThreadParticipant, len(ps))
	for i, p := range ps {
		out[i] = apitypes.ThreadParticipant{Kind: apitypes.ThreadParticipantKind(p.Kind), Id: p.ID, Name: p.Name, LastSentAt: p.LastSentAt.UnixMilli()}
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

// createAgentThread starts a thread under a platform-chosen ID.
func (a *api) createAgentThread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	var in apitypes.AgentThreadCreate
	if r.ContentLength != 0 && !httpx.Decode(w, r, &in) {
		return
	}
	var visibility, title string
	if in.Visibility != nil {
		visibility = string(*in.Visibility)
	}
	if in.Title != nil {
		title = *in.Title
	}
	t, err := a.Agents.CreateThread(r.Context(), a.actor(r), id, visibility, title)
	if failed(w, r, err) {
		return
	}
	w.Header().Set("Location", "/v1/agents/"+id.String()+"/threads/"+t.ThreadID)
	httpx.JSON(w, http.StatusCreated, toAPIThread(t))
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

// adminListAgentThreads lists every thread of an agent for platform admins
// (titles of private and personal-workspace threads withheld).
func (a *api) adminListAgentThreads(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	rows, err := a.Agents.AdminThreads(r.Context(), a.actor(r), id)
	writeList(w, r, rows, err, toAPIAdminThread)
}

// adminReadAgentThread relays a read of the thread to the agent's runtime
// for a platform admin with a reason, and records it in the agent's team's
// audit log before returning the conversation (the runtime's answer is held
// until then; nothing is recorded when the runtime has no such thread).
func (a *api) adminReadAgentThread(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "agentId")
	if !ok {
		return
	}
	threadID := r.PathValue("threadId")
	if !threadIDRE.MatchString(threadID) {
		httpx.Error(w, http.StatusBadRequest, "invalid_thread_id", "Thread IDs are 1-64 letters, digits, dashes or underscores")
		return
	}
	var in apitypes.AdminThreadRead
	if !httpx.Decode(w, r, &in) {
		return
	}
	read, err := a.Agents.AdminRead(r.Context(), a.actor(r), id, threadID, in.Reason)
	if failed(w, r, err) {
		return
	}
	get := r.Clone(r.Context())
	get.Method, get.Body, get.ContentLength, get.Header = http.MethodGet, http.NoBody, 0, http.Header{}
	get.URL.RawQuery = ""
	held := &heldResponse{header: http.Header{}}
	a.Agents.Host().Proxy(held, get, read.Spec, "/threads/"+threadID)
	if held.status == 0 {
		httpx.Error(w, http.StatusBadGateway, "agent_unavailable", "The agent did not answer. Try again shortly.")
		return
	}
	if held.status >= 200 && held.status < 300 && failed(w, r, a.Agents.RecordAdminRead(r.Context(), read)) {
		return
	}
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if v := held.header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(held.status)
	_, _ = w.Write(held.body.Bytes())
}

// heldResponse keeps a relayed response in memory.
type heldResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *heldResponse) Header() http.Header { return h.header }

func (h *heldResponse) WriteHeader(code int) {
	if h.status == 0 {
		h.status = code
	}
}

func (h *heldResponse) Write(p []byte) (int, error) {
	h.WriteHeader(http.StatusOK)
	return h.body.Write(p)
}

func (h *heldResponse) Flush() {}
