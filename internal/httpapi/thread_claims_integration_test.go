package httpapi_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// threadClaimEnv is a team with an agent that takes MCP servers from apps
// (and one that does not), and apps allowed on 127.0.0.1.
type threadClaimEnv struct {
	*teamEnv
	agent, plain string // agent IDs
	base         string // /v1/agents/<agent>
}

func newThreadClaimEnv(t *testing.T) *threadClaimEnv {
	t.Helper()
	app := newTestAppWith(t, func(c *config.Config) { c.Agents.ClientMCPHosts = "http://127.0.0.1:*" }, testutil.Logger(), nil, false)
	e := &threadClaimEnv{teamEnv: newTeamEnvOn(t, app)}
	var tpl apitypes.AgentTemplate
	code, ec := e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "apps", "name": "Apps", "instructions": "Hi.", "clientMcp": true}, &tpl, nil)
	mustCode(t, "template with clientMcp", code, ec, 201, "")
	var ag, plain apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Apps desk", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Plain desk"}, &plain, nil)
	mustCode(t, "deploy plain", code, ec, 201, "")
	e.agent, e.plain, e.base = ag.Id.String(), plain.Id.String(), "/v1/agents/"+ag.Id.String()
	return e
}

// servers is a request body for PUT .../mcp-servers.
func mcpServersBody(visibility string, names ...string) map[string]any {
	list := []any{}
	for _, n := range names {
		list = append(list, map[string]any{"name": n, "url": "http://127.0.0.1:9/mcp", "headers": map[string]string{"Authorization": "Bearer secret-of-" + n}})
	}
	body := map[string]any{"servers": list}
	if visibility != "" {
		body["visibility"] = visibility
	}
	return body
}

func (e *threadClaimEnv) agentUUID() uuid.UUID { return uuid.MustParse(e.agent) }

func (e *threadClaimEnv) record(agent, thread string) (creator, visibility, reserved string) {
	e.t.Helper()
	var c, v, r string
	err := e.app.Pool.QueryRow(context.Background(), `SELECT coalesce(created_by_user::text, ''), visibility, reserved::text
		FROM agent_threads WHERE agent_id = $1 AND thread_id = $2`, agent, thread).Scan(&c, &v, &r)
	if err != nil {
		return "", "", ""
	}
	return c, v, r
}

func (e *threadClaimEnv) serverCount(agent, thread string) string {
	return e.scalar(`SELECT count(*)::text FROM thread_mcp_servers WHERE agent_id = $1 AND thread_id = $2`, agent, thread)
}

func (e *threadClaimEnv) threadRecords(agent, thread string) string {
	return e.scalar(`SELECT count(*)::text FROM agent_threads WHERE agent_id = $1 AND thread_id = $2`, agent, thread)
}

// A person can't attach MCP servers to another person's future private
// thread: servers are set on a thread the platform records, with the person
// who set them as its creator, so whoever uses the ID later gets the normal
// rules of the thread they find.
func TestThreadMCPServersClaimTheThread(t *testing.T) {
	e := newThreadClaimEnv(t)
	a, b := e.member, e.editor
	aID := a.me.User.Id.String()

	// The attack: A sets servers on an ID nobody has used, hoping B's later
	// private thread under that ID inherits them.
	code, ec := a.call("PUT", e.base+"/threads/x1/mcp-servers", mcpServersBody("", "Evil"), nil, nil)
	mustCode(t, "A sets servers on an unused ID", code, ec, 200, "")
	if creator, vis, reserved := e.record(e.agent, "x1"); creator != aID || vis != "shared" || reserved != "true" {
		t.Fatalf("the thread's record: creator %q visibility %q reserved %q", creator, vis, reserved)
	}
	// B can't start a private thread on that ID: it is A's (shared) thread.
	code, ec = b.call("POST", e.base+"/threads/x1/messages", map[string]any{"text": "My private chat", "visibility": "private"}, nil, nil)
	mustCode(t, "B starts a private thread on A's ID", code, ec, 409, "visibility_fixed")
	if got := e.serverCount(e.agent, "x1"); got != "1" {
		t.Fatalf("servers after B's refused message: %s", got)
	}
	// A shared thread is shared: B may join it, sees whose it is, and sees the servers' names (never headers).
	var th []apitypes.AgentThread
	if code := b.get(e.base+"/threads", &th); code != 200 || len(th) != 0 {
		t.Fatalf("B lists a thread nobody has sent to yet: %d %+v", code, th)
	}
	var list []apitypes.ThreadMcpServer
	code, raw := b.raw("GET", e.base+"/threads/x1/mcp-servers", nil, nil)
	if code != 200 || strings.Contains(string(raw), "secret-of") {
		t.Fatalf("B reads the shared thread's servers: %d %s", code, raw)
	}
	mustDecode(t, raw, &list)
	if len(list) != 1 || list[0].Name != "Evil" {
		t.Fatalf("servers %+v", list)
	}

	// The same with a private thread: A's servers, A's private thread.
	code, ec = a.call("PUT", e.base+"/threads/x2/mcp-servers", mcpServersBody("private", "Mine"), nil, nil)
	mustCode(t, "A starts a private thread with servers", code, ec, 200, "")
	if _, vis, _ := e.record(e.agent, "x2"); vis != "private" {
		t.Fatalf("visibility %q", vis)
	}
	for who, s := range map[string]*session{"B": b, "the owner": e.owner} {
		for _, c := range []struct{ method, path string }{
			{"GET", "/threads/x2/mcp-servers"}, {"PUT", "/threads/x2/mcp-servers"}, {"POST", "/threads/x2/messages"},
		} {
			var body any
			switch c.method {
			case "PUT":
				body = mcpServersBody("", "Theirs")
			case "POST":
				body = map[string]any{"text": "hello", "visibility": "private"}
			}
			code, ec := s.call(c.method, e.base+c.path, body, nil, nil)
			mustCode(t, who+" "+c.method+" "+c.path, code, ec, 404, "thread_not_found")
		}
	}
	if got := e.serverCount(e.agent, "x2"); got != "1" {
		t.Fatalf("A's servers changed: %s", got)
	}

	// Without servers a thread isn't started for nobody's sake: an empty list on an unknown ID changes nothing.
	code, ec = a.call("PUT", e.base+"/threads/x3/mcp-servers", mcpServersBody(""), nil, nil)
	mustCode(t, "no servers on an unused ID", code, ec, 200, "")
	if got := e.threadRecords(e.agent, "x3"); got != "0" {
		t.Fatalf("a thread was started for an empty list: %s", got)
	}
	code, raw = b.raw("GET", e.base+"/threads/x3/mcp-servers", nil, nil)
	if code != 200 || strings.TrimSpace(string(raw)) != `{"data":[]}` {
		t.Fatalf("servers of an unknown thread: %d %s", code, raw)
	}

	// Bad requests start nothing: a bad visibility, a host off the allowlist, a template that takes none.
	code, ec = a.call("PUT", e.base+"/threads/x4/mcp-servers", mcpServersBody("everyone", "Mine"), nil, nil)
	mustCode(t, "bad visibility", code, ec, 400, "invalid_visibility")
	code, ec = a.call("PUT", e.base+"/threads/x4/mcp-servers", map[string]any{"servers": []any{map[string]any{"name": "Evil", "url": "https://evil.example.com/mcp"}}}, nil, nil)
	mustCode(t, "a host not on the allowlist", code, ec, 400, "client_mcp_host_not_allowed")
	code, ec = a.call("PUT", "/v1/agents/"+e.plain+"/threads/x4/mcp-servers", mcpServersBody("", "Mine"), nil, nil)
	mustCode(t, "a template without clientMcp", code, ec, 409, "client_mcp_not_allowed")
	for _, agent := range []string{e.agent, e.plain} {
		if got := e.threadRecords(agent, "x4"); got != "0" {
			t.Fatalf("a refused request started a thread on %s: %s", agent, got)
		}
	}
	// Someone who may not use the agent starts nothing either.
	code, ec = e.outsider.call("PUT", e.base+"/threads/x4/mcp-servers", mcpServersBody("", "Mine"), nil, nil)
	if code != 404 && code != 403 {
		t.Fatalf("outsider: %d %s", code, ec)
	}
	if got := e.threadRecords(e.agent, "x4"); got != "0" {
		t.Fatalf("an outsider started a thread: %s", got)
	}

	// The app flow still works: servers first, then the first message (visibility sent with the servers).
	code, ec = a.call("PUT", e.base+"/threads/app1/mcp-servers", mcpServersBody("private", "Tools"), nil, nil)
	mustCode(t, "servers before the first message", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads/app1/messages", map[string]any{"text": "Use my tools", "visibility": "private"}, nil, nil)
	mustCode(t, "first message of the thread", code, ec, 202, "")
	waitReply(t, a, e.base+"/threads/app1", 2)
	if creator, vis, reserved := e.record(e.agent, "app1"); creator != aID || vis != "private" || reserved != "false" {
		t.Fatalf("record after the first message: %q %q %q", creator, vis, reserved)
	}
	if got := e.serverCount(e.agent, "app1"); got != "1" {
		t.Fatalf("servers after the first message: %s", got)
	}
	// The visibility chosen with the servers can't be changed by the message.
	code, ec = a.call("PUT", e.base+"/threads/app2/mcp-servers", mcpServersBody("private", "Tools"), nil, nil)
	mustCode(t, "servers of app2", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads/app2/messages", map[string]any{"text": "Hello", "visibility": "shared"}, nil, nil)
	mustCode(t, "a different visibility", code, ec, 409, "visibility_fixed")
	// And the first message's own servers work as before (the thread is started by the message).
	code, ec = a.call("POST", e.base+"/threads/app3/messages", map[string]any{"text": "Hi", "visibility": "private",
		"mcpServers": mcpServersBody("", "Inline")["servers"]}, nil, nil)
	mustCode(t, "a message with servers", code, ec, 202, "")
	if got := e.serverCount(e.agent, "app3"); got != "1" {
		t.Fatalf("inline servers: %s", got)
	}
	code, ec = b.call("PUT", e.base+"/threads/app3/mcp-servers", mcpServersBody("", "Theirs"), nil, nil)
	mustCode(t, "B sets servers on A's private thread", code, ec, 404, "thread_not_found")

	// A refused first message takes its servers with it (the thread record it started).
	code, ec = a.call("POST", e.base+"/threads/app4/messages", map[string]any{"text": "Hi",
		"mcpServers": []any{map[string]any{"name": "Evil", "url": "https://evil.example.com/mcp"}}}, nil, nil)
	mustCode(t, "message with a bad server", code, ec, 400, "client_mcp_host_not_allowed")
	if got := e.threadRecords(e.agent, "app4"); got != "0" {
		t.Fatalf("a refused message left a thread: %s", got)
	}

	// Servers go with their thread: deleting the thread deletes them.
	code, ec = a.call("POST", e.base+"/threads/gone/messages", map[string]any{"text": "Hi"}, nil, nil)
	mustCode(t, "start the thread to delete", code, ec, 202, "")
	waitReply(t, a, e.base+"/threads/gone", 2)
	code, ec = a.call("PUT", e.base+"/threads/gone/mcp-servers", mcpServersBody("", "Tools"), nil, nil)
	mustCode(t, "servers on the thread", code, ec, 200, "")
	code, ec = a.call("DELETE", e.base+"/threads/gone", nil, nil, nil)
	mustCode(t, "delete the thread", code, ec, 200, "")
	if got := e.serverCount(e.agent, "gone"); got != "0" {
		t.Fatalf("a deleted thread's servers remain: %s", got)
	}
	// The ID is free again: someone else starts a private thread there and inherits nothing.
	code, ec = b.call("POST", e.base+"/threads/gone/messages", map[string]any{"text": "Mine now", "visibility": "private"}, nil, nil)
	mustCode(t, "B starts a private thread on the freed ID", code, ec, 202, "")
	code, raw = b.raw("GET", e.base+"/threads/gone/mcp-servers", nil, nil)
	if code != 200 || strings.TrimSpace(string(raw)) != `{"data":[]}` {
		t.Fatalf("the new thread's servers: %d %s", code, raw)
	}
}

var platformThreadID = regexp.MustCompile(`^t_[A-Za-z0-9_-]{22}$`)

// POST .../threads starts a thread under an ID the platform chooses.
func TestCreateAgentThread(t *testing.T) {
	e := newThreadClaimEnv(t)
	a, b := e.member, e.editor
	aID := a.me.User.Id.String()

	var one, two apitypes.AgentThread
	res, raw := a.raw("POST", e.base+"/threads", nil, nil)
	if res != http.StatusCreated {
		t.Fatalf("create: %d %s", res, raw)
	}
	mustDecode(t, raw, &one)
	code, ec := a.call("POST", e.base+"/threads", map[string]any{"visibility": "private", "title": "  Quarterly plan "}, &two, nil)
	mustCode(t, "create a private, titled thread", code, ec, 201, "")
	for _, th := range []apitypes.AgentThread{one, two} {
		if !platformThreadID.MatchString(th.ThreadId) {
			t.Fatalf("thread ID %q", th.ThreadId)
		}
		if b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(th.ThreadId, "t_")); err != nil || len(b) != 16 {
			t.Fatalf("thread ID %q is not 128 random bits: %v", th.ThreadId, err)
		}
		if th.CreatedBy == nil || th.CreatedBy.Id.String() != aID || th.Busy || len(th.Participants) != 0 {
			t.Fatalf("thread %+v", th)
		}
	}
	if one.ThreadId == two.ThreadId {
		t.Fatal("two threads got one ID")
	}
	if one.Visibility != "shared" || one.Title != "New chat" || two.Visibility != "private" || two.Title != "Quarterly plan" {
		t.Fatalf("threads %+v %+v", one, two)
	}
	for _, c := range []struct {
		what string
		body any
		code string
	}{
		{"bad visibility", map[string]any{"visibility": "everyone"}, "invalid_visibility"},
		{"bad title", map[string]any{"title": "two\nlines"}, "invalid_title"},
		{"unknown field", map[string]any{"threadId": "mine"}, "invalid_json"},
	} {
		code, ec := a.call("POST", e.base+"/threads", c.body, nil, nil)
		mustCode(t, c.what, code, ec, 400, c.code)
	}

	// Unstarted threads are listed for their creator only; anyone may still use a shared one by its ID.
	ids := func(s *session) map[string]apitypes.AgentThread {
		var list []apitypes.AgentThread
		if code := s.get(e.base+"/threads", &list); code != 200 {
			t.Fatalf("list: %d", code)
		}
		return threadIDs(list)
	}
	if got := ids(a); len(got) != 2 || got[one.ThreadId].Title != "New chat" {
		t.Fatalf("A's list %+v", got)
	}
	if got := ids(b); len(got) != 0 {
		t.Fatalf("B lists A's unstarted threads: %+v", got)
	}
	// A private thread is A's alone, on every route, before its first message too.
	for _, c := range []struct {
		method, suffix string
		body           any
	}{
		{"POST", "/messages", map[string]any{"text": "hi"}}, {"GET", "/mcp-servers", nil},
		{"PUT", "/mcp-servers", map[string]any{"servers": []any{}}}, {"PATCH", "", map[string]any{"title": "x"}},
	} {
		code, ec := b.call(c.method, e.base+"/threads/"+two.ThreadId+c.suffix, c.body, nil, nil)
		mustCode(t, "B "+c.method+" "+c.suffix, code, ec, 404, "thread_not_found")
	}
	// The first message: keeps the visibility, names the thread, ends the reservation.
	code, ec = a.call("POST", e.base+"/threads/"+two.ThreadId+"/messages", map[string]any{"text": "Plan the quarter"}, nil, nil)
	mustCode(t, "first message of the private thread", code, ec, 202, "")
	waitReply(t, a, e.base+"/threads/"+two.ThreadId, 2)
	if got := ids(a)[two.ThreadId]; got.Visibility != "private" || got.Title != "Quarterly plan" || len(got.Participants) != 1 {
		t.Fatalf("after the first message %+v", got)
	}
	if got := ids(b); len(got) != 0 {
		t.Fatalf("B lists A's private thread: %+v", got)
	}
	// A shared thread: B joins by ID; the first message names it, and it is listed for everyone then.
	code, ec = b.call("POST", e.base+"/threads/"+one.ThreadId+"/messages", map[string]any{"text": "Lunch ideas\nplease"}, nil, nil)
	mustCode(t, "B joins A's shared thread", code, ec, 202, "")
	waitReply(t, b, e.base+"/threads/"+one.ThreadId, 2)
	got := ids(a)[one.ThreadId]
	if got.Title != "Lunch ideas" || got.CreatedBy == nil || got.CreatedBy.Id.String() != aID || len(got.Participants) != 1 || got.Participants[0].Id != b.me.User.Id {
		t.Fatalf("shared thread after B's message %+v", got)
	}
	if ids(b)[one.ThreadId].ThreadId == "" {
		t.Fatal("B doesn't list the shared thread once it has a message")
	}

	// Authorization is sending a message's: others' agents and turned-off agents refuse.
	code, ec = e.outsider.call("POST", e.base+"/threads", nil, nil, nil)
	if code != 404 && code != 403 {
		t.Fatalf("outsider: %d %s", code, ec)
	}
	code, ec = e.outsider.call("POST", "/v1/agents/00000000-0000-0000-0000-000000000000/threads", nil, nil, nil)
	mustCode(t, "unknown agent", code, ec, 404, "")
	var ag apitypes.Agent
	e.editor.get(e.base, &ag)
	code, ec = e.editor.call("PATCH", e.base, map[string]any{"enabled": false}, nil, ifMatch(ag.Revision))
	mustCode(t, "turn the agent off", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads", nil, nil, nil)
	mustCode(t, "a turned-off agent", code, ec, 409, "agent_disabled")
}

// Threads the platform started and nobody sent to are not the runtime's to
// forget, and are removed after 30 days (with their servers).
func TestReservedThreadsLifecycle(t *testing.T) {
	e := newThreadClaimEnv(t)
	a := e.member
	var kept, abandoned apitypes.AgentThread
	code, ec := a.call("POST", e.base+"/threads", nil, &kept, nil)
	mustCode(t, "create", code, ec, 201, "")
	code, ec = a.call("POST", e.base+"/threads", nil, &abandoned, nil)
	mustCode(t, "create", code, ec, 201, "")
	code, ec = a.call("PUT", e.base+"/threads/"+abandoned.ThreadId+"/mcp-servers", mcpServersBody("", "Tools"), nil, nil)
	mustCode(t, "servers", code, ec, 200, "")

	ctx := context.Background()
	if _, err := e.app.Pool.Exec(ctx, `UPDATE agent_threads SET created_at = now() - interval '2 hours', status_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	// The runtime lists the threads it has when it starts: it has neither.
	agentID := e.agentUUID()
	if err := e.app.Agents.SyncThreads(ctx, agentID, nil, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{kept.ThreadId, abandoned.ThreadId} {
		if got := e.threadRecords(e.agent, id); got != "1" {
			t.Fatalf("the runtime's list forgot reserved thread %s", id)
		}
	}
	// Thirty days later the abandoned one goes, with its servers.
	if _, err := e.app.Pool.Exec(ctx, `UPDATE agent_threads SET created_at = now() - interval '31 days' WHERE thread_id = $1`, abandoned.ThreadId); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Agents.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if e.threadRecords(e.agent, abandoned.ThreadId) != "0" || e.serverCount(e.agent, abandoned.ThreadId) != "0" || e.threadRecords(e.agent, kept.ThreadId) != "1" {
		t.Fatal("clean-up removed the wrong threads")
	}
}
