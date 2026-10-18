package httpapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// threadEvents follows /v1/agents/events as s, collecting thread events.
type threadEvents struct {
	mu  sync.Mutex
	got []apitypes.AgentThreadEvent
}

func followThreads(t *testing.T, s *session) *threadEvents {
	t.Helper()
	req, _ := http.NewRequest("GET", s.app.URL+"/v1/agents/events", nil)
	res, err := s.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, res)
	}
	t.Cleanup(func() { res.Body.Close() })
	ev := &threadEvents{}
	go func() {
		sc := bufio.NewScanner(res.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok && event == "thread" {
				var e apitypes.AgentThreadEvent
				if json.Unmarshal([]byte(v), &e) == nil {
					ev.mu.Lock()
					ev.got = append(ev.got, e)
					ev.mu.Unlock()
				}
			}
		}
	}()
	return ev
}

// threads are the IDs of the threads events were heard for.
func (e *threadEvents) threads() map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]bool{}
	for _, ev := range e.got {
		out[ev.ThreadId] = true
	}
	return out
}

// waitFor waits until an event for threadID was heard.
func (e *threadEvents) waitFor(t *testing.T, threadID string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); sleep100() {
		if e.threads()[threadID] {
			return
		}
	}
	t.Fatalf("no event for %s: %v", threadID, e.threads())
}

func threadIDs(ts []apitypes.AgentThread) map[string]apitypes.AgentThread {
	out := map[string]apitypes.AgentThread{}
	for _, th := range ts {
		out[th.ThreadId] = th
	}
	return out
}

// Private and shared threads: a private thread is its creator's alone (not
// other members', not the team owner's), on every route, in listings and in
// event streams; messages record their authors; platform admins read any
// conversation only with a reason, which the owner sees in the audit log.
func TestPrivateThreads(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	e := newTeamEnvOn(t, app)
	a, b, owner := e.member, e.editor, e.owner // member A (blair), member B (alex), the team's owner
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Shared desk"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	aEvents, bEvents, ownerEvents := followThreads(t, a), followThreads(t, b), followThreads(t, owner)

	// A starts a private thread.
	code, ec = a.call("POST", base+"/threads/p1/messages", map[string]any{"text": "My secret plan", "visibility": "private"}, nil, nil)
	mustCode(t, "private thread", code, ec, 202, "")
	view := waitReply(t, a, base+"/threads/p1", 2)
	if au := view.Messages[0].Author; au == nil || au.Id != a.me.User.Id.String() || au.Kind != "user" || au.Via != nil || view.Messages[0].At == nil {
		t.Fatalf("A's message %+v", view.Messages[0])
	}
	if m := view.Messages[1]; m.At == nil || m.Model == nil || m.StopReason == nil || m.Usage == nil {
		t.Fatalf("assistant metadata %+v", m)
	}

	// Nobody else reaches it, on any route: it reads as not found.
	for who, s := range map[string]*session{"member B": b, "the owner": owner} {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", base + "/threads/p1", nil},
			{"GET", base + "/threads/p1/events?once=true", nil},
			{"POST", base + "/threads/p1/messages", map[string]any{"text": "let me in"}},
			{"POST", base + "/threads/p1/messages", map[string]any{"text": "let me in", "visibility": "shared"}},
			{"POST", base + "/threads/p1/stop", nil},
			{"PATCH", base + "/threads/p1", map[string]any{"title": "Mine now"}},
			{"DELETE", base + "/threads/p1", nil},
			{"GET", base + "/threads/p1/mcp-servers", nil},
			{"PUT", base + "/threads/p1/mcp-servers", map[string]any{"servers": []any{}}},
		} {
			code, ec := s.call(c.method, c.path, c.body, nil, nil)
			mustCode(t, who+" "+c.method+" "+c.path, code, ec, 404, "thread_not_found")
		}
	}

	// A shared thread: B joins in; a client can't claim to be someone else.
	code, ec = a.call("POST", base+"/threads/s1/messages", map[string]any{"text": "Hello all"}, nil, nil)
	mustCode(t, "shared thread", code, ec, 202, "")
	waitReply(t, a, base+"/threads/s1", 2)
	code, ec = b.call("POST", base+"/threads/s1/messages", map[string]any{"text": "Me too",
		"author": map[string]any{"kind": "user", "id": owner.me.User.Id.String(), "name": "The Owner"}, "speaker": "The Owner"}, nil, nil)
	mustCode(t, "B sends to the shared thread", code, ec, 202, "")
	view = waitReply(t, b, base+"/threads/s1", 4)
	if au := view.Messages[0].Author; au == nil || au.Id != a.me.User.Id.String() {
		t.Fatalf("first author %+v", view.Messages[0])
	}
	if au := view.Messages[2].Author; au == nil || au.Id != b.me.User.Id.String() || au.Name == "The Owner" || text(view.Messages[2]) != "Me too" {
		t.Fatalf("B's message %+v (author %+v)", view.Messages[2], au)
	}

	// Listings: everyone sees the shared thread with its participants; only A sees p1.
	var listed []apitypes.AgentThread
	if code := b.get(base+"/threads", &listed); code != 200 {
		t.Fatalf("B lists: %d", code)
	}
	got := threadIDs(listed)
	if _, ok := got["p1"]; ok {
		t.Fatalf("B lists A's private thread: %+v", listed)
	}
	s1 := got["s1"]
	if s1.Visibility != "shared" || s1.CreatedBy == nil || s1.CreatedBy.Id != a.me.User.Id || len(s1.Participants) != 2 ||
		s1.Participants[0].Id != a.me.User.Id || s1.Participants[1].Id != b.me.User.Id || s1.Participants[1].LastSentAt == 0 {
		t.Fatalf("shared thread %+v", s1)
	}
	if code := owner.get(base+"/threads", &listed); code != 200 || threadIDs(listed)["p1"].ThreadId != "" {
		t.Fatalf("the owner lists %d %+v", code, listed)
	}
	if code := a.get(base+"/threads", &listed); code != 200 {
		t.Fatalf("A lists: %d", code)
	}
	p1 := threadIDs(listed)["p1"]
	if p1.Visibility != "private" || p1.CreatedBy == nil || p1.CreatedBy.Kind != "user" || p1.CreatedBy.Id != a.me.User.Id ||
		p1.Title != "My secret plan" || len(p1.Participants) != 1 {
		t.Fatalf("A's private thread %+v", p1)
	}

	// Event streams: A hears p1; B and the owner hear s1 (after p1's events) but never p1.
	aEvents.waitFor(t, "p1")
	bEvents.waitFor(t, "s1")
	ownerEvents.waitFor(t, "s1")
	if bEvents.threads()["p1"] || ownerEvents.threads()["p1"] {
		t.Fatalf("others heard the private thread: B %v, owner %v", bEvents.threads(), ownerEvents.threads())
	}

	// Visibility is fixed with the first message.
	code, ec = a.call("POST", base+"/threads/p1/messages", map[string]any{"text": "now public", "visibility": "shared"}, nil, nil)
	mustCode(t, "make a private thread shared", code, ec, 409, "visibility_fixed")
	code, ec = a.call("POST", base+"/threads/s1/messages", map[string]any{"text": "now private", "visibility": "private"}, nil, nil)
	mustCode(t, "make a shared thread private", code, ec, 409, "visibility_fixed")
	code, ec = a.call("POST", base+"/threads/x1/messages", map[string]any{"text": "hm", "visibility": "secret"}, nil, nil)
	mustCode(t, "an unknown visibility", code, ec, 400, "invalid_visibility")
	code, ec = a.call("POST", base+"/threads/p1/messages", map[string]any{"text": "still mine", "visibility": "private"}, nil, nil)
	mustCode(t, "the same visibility again", code, ec, 202, "")
	waitReply(t, a, base+"/threads/p1", 4)

	// A refused first message leaves no record behind.
	code, _ = a.call("POST", base+"/threads/r1/messages", map[string]any{"text": "x", "visibility": "private", "requestId": "bad id!"}, nil, nil)
	if code != 400 {
		t.Fatalf("refused message: %d", code)
	}
	if n := e.scalar(`SELECT count(*)::text FROM agent_threads WHERE agent_id = $1 AND thread_id = 'r1'`, ag.Id); n != "0" {
		t.Fatalf("refused thread recorded: %s", n)
	}

	// A service account's private thread is its own.
	sa := keyClient{t, app.URL, createSAKey(t, owner, createSA(t, owner, map[string]any{"name": "helpdesk-bot"}).Id.String(), map[string]any{"name": "k"}).Secret}
	code, ec = sa.call("POST", base+"/threads/b1/messages", map[string]any{"text": "Ticket 42", "visibility": "private"}, nil)
	mustCode(t, "service account private thread", code, ec, 202, "")
	bv := waitReplyKey(t, sa, base+"/threads/b1", 2)
	if au := bv.Messages[0].Author; au == nil || au.Kind != "service_account" || au.Name != "helpdesk-bot" {
		t.Fatalf("service account's message %+v", bv.Messages[0])
	}
	for who, s := range map[string]*session{"A": a, "the owner": owner} {
		code, ec := s.call("GET", base+"/threads/b1", nil, nil, nil)
		mustCode(t, who+" reads the service account's thread", code, ec, 404, "thread_not_found")
		s.get(base+"/threads", &listed)
		if threadIDs(listed)["b1"].ThreadId != "" {
			t.Fatalf("%s lists the service account's thread", who)
		}
	}
	var saListed []apitypes.AgentThread
	sa.call("GET", base+"/threads", nil, &saListed)
	if b1 := threadIDs(saListed)["b1"]; b1.CreatedBy == nil || b1.CreatedBy.Kind != "service_account" || threadIDs(saListed)["p1"].ThreadId != "" {
		t.Fatalf("service account lists %+v", saListed)
	}

	// An app (OAuth Connect) acting for A: the thread is A's, sent via the app.
	regBody, _ := json.Marshal(map[string]any{"client_name": "Pocket Chat", "redirect_uris": []string{"http://127.0.0.1/cb"},
		"token_endpoint_auth_method": "none"})
	res, err := http.Post(app.URL+"/oauth/register", "application/json", bytes.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	_ = json.NewDecoder(res.Body).Decode(&reg)
	res.Body.Close()
	token := connect(t, oauthFlow{t, app}, a, reg["client_id"].(string), "st")
	code, ec, _ = bearer(t, app, token, "POST", base+"/threads/o1/messages", map[string]any{"text": "From my phone", "visibility": "private"}, nil)
	mustCode(t, "app's private thread", code, ec, 202, "")
	view = waitReply(t, a, base+"/threads/o1", 2)
	if au := view.Messages[0].Author; au == nil || au.Id != a.me.User.Id.String() || au.Via == nil || *au.Via != "Pocket Chat" {
		t.Fatalf("app's message %+v", view.Messages[0].Author)
	}
	a.get(base+"/threads", &listed)
	if o1 := threadIDs(listed)["o1"]; o1.CreatedBy == nil || o1.CreatedBy.Id != a.me.User.Id || o1.Visibility != "private" {
		t.Fatalf("app's thread %+v", o1)
	}
	code, ec = b.call("GET", base+"/threads/o1", nil, nil, nil)
	mustCode(t, "B reads the app's private thread", code, ec, 404, "thread_not_found")

	// Platform admins list every thread; private titles are withheld.
	var adminList []apitypes.AdminAgentThread
	if code := e.admin.get("/v1/admin/agents/"+ag.Id.String()+"/threads", &adminList); code != 200 {
		t.Fatalf("admin lists: %d", code)
	}
	byID := map[string]apitypes.AdminAgentThread{}
	for _, th := range adminList {
		byID[th.ThreadId] = th
	}
	if p := byID["p1"]; p.Title != nil || !p.TitleHidden || p.Visibility != "private" || p.CreatedBy == nil {
		t.Fatalf("admin's private thread %+v", p)
	}
	if s := byID["s1"]; s.Title == nil || *s.Title != "Hello all" || s.TitleHidden || len(s.Participants) != 2 {
		t.Fatalf("admin's shared thread %+v", s)
	}
	code, ec = e.auditor.call("GET", "/v1/admin/agents/"+ag.Id.String()+"/threads", nil, nil, nil)
	mustCode(t, "an auditor lists", code, ec, 403, "forbidden")
	// Their usual access is unchanged: they don't converse with a team's agents.
	code, ec = e.admin.call("GET", base+"/threads/p1", nil, nil, nil)
	mustCode(t, "an admin reads directly", code, ec, 404, "agent_not_found")

	// A reasoned read, audited where the team's owners see it.
	readPath := "/v1/admin/agents/" + ag.Id.String() + "/threads/p1/read"
	code, ec = e.admin.call("POST", readPath, map[string]any{}, nil, nil)
	mustCode(t, "a read without a reason", code, ec, 400, "invalid_reason")
	code, ec = e.admin.call("POST", readPath, map[string]any{"reason": "short"}, nil, nil)
	mustCode(t, "a read with a short reason", code, ec, 400, "invalid_reason")
	code, ec = e.auditor.call("POST", readPath, map[string]any{"reason": "Looking into a complaint"}, nil, nil)
	mustCode(t, "an auditor reads", code, ec, 403, "forbidden")
	var read apitypes.ChatView
	code, ec = e.admin.call("POST", readPath, map[string]any{"reason": "Abuse report #17 names this thread"}, &read, nil)
	mustCode(t, "an admin reads with a reason", code, ec, 200, "")
	if len(read.Messages) < 4 || text(read.Messages[0]) != "My secret plan" {
		t.Fatalf("admin read %+v", read)
	}
	var log apitypes.AuditPage
	if code := owner.get(toolsTeam+"/audit?action=thread.admin_read", &log); code != 200 || len(log.Items) != 1 {
		t.Fatalf("team audit %d %+v", code, log)
	}
	entry := log.Items[0]
	if entry.TargetType != "agent_thread" || entry.TargetId != ag.Id.String()+"/p1" || entry.Metadata["reason"] != "Abuse report #17 names this thread" ||
		entry.Metadata["visibility"] != "private" || entry.Metadata["personal"] != false || entry.TargetLabel == nil ||
		!strings.Contains(*entry.TargetLabel, "Shared desk") || entry.ActorUserId == nil || *entry.ActorUserId != e.admin.me.User.Id {
		t.Fatalf("audit entry %+v", entry)
	}
	code, ec = e.admin.call("POST", "/v1/admin/agents/"+ag.Id.String()+"/threads/nope/read", map[string]any{"reason": "Checking a missing thread"}, nil, nil)
	mustCode(t, "a read of a thread that doesn't exist", code, ec, 404, "thread_not_found")
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'thread.admin_read'`); n != "1" {
		t.Fatalf("admin reads recorded: %s", n)
	}

	// A personal workspace's agent: its conversations reach platform admins
	// only through a reasoned read; its titles are withheld.
	var mine []apitypes.MyTeam
	casey := e.outsider
	casey.get("/v1/teams", &mine)
	ws := ""
	for _, tm := range mine {
		if tm.Kind != nil && *tm.Kind == "personal" {
			ws = tm.Slug
		}
	}
	var pag apitypes.Agent
	code, ec = casey.call("POST", "/v1/teams/"+ws+"/agents", map[string]any{"name": "Diary"}, &pag, nil)
	mustCode(t, "personal agent", code, ec, 201, "")
	pbase := "/v1/agents/" + pag.Id.String()
	code, ec = casey.call("POST", pbase+"/threads/d1/messages", map[string]any{"text": "Dear diary"}, nil, nil)
	mustCode(t, "personal thread", code, ec, 202, "")
	waitReply(t, casey, pbase+"/threads/d1", 2)
	casey.get(pbase+"/threads", &listed)
	if d1 := threadIDs(listed)["d1"]; d1.Visibility != "shared" {
		t.Fatalf("personal thread %+v", d1)
	}
	code, ec = e.admin.call("GET", pbase+"/threads/d1", nil, nil, nil)
	mustCode(t, "an admin reads a personal conversation directly", code, ec, 404, "agent_not_found")
	if code := e.admin.get("/v1/admin/agents/"+pag.Id.String()+"/threads", &adminList); code != 200 || len(adminList) != 1 ||
		adminList[0].Title != nil || !adminList[0].TitleHidden {
		t.Fatalf("admin lists a personal agent's threads %d %+v", code, adminList)
	}
	code, ec = e.admin.call("POST", "/v1/admin/agents/"+pag.Id.String()+"/threads/d1/read", map[string]any{"reason": "Legal hold request 2026-114"}, &read, nil)
	mustCode(t, "an admin reads a personal conversation with a reason", code, ec, 200, "")
	if text(read.Messages[0]) != "Dear diary" {
		t.Fatalf("personal read %+v", read)
	}
	if code := casey.get("/v1/teams/"+ws+"/audit?action=thread.admin_read", &log); code != 200 || len(log.Items) != 1 ||
		log.Items[0].Metadata["personal"] != true || log.Items[0].Metadata["reason"] != "Legal hold request 2026-114" {
		t.Fatalf("personal audit %d %+v", code, log)
	}
}
