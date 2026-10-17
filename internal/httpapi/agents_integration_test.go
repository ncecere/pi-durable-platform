package httpapi_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// A team agent: deployed by an editor, tested by the team (the platform
// starts its runtime), private to the team, and surviving hibernation.
func TestTeamAgent(t *testing.T) {
	e := newTeamEnv(t)

	var ag apitypes.Agent
	code, ec := e.member.call("POST", toolsTeam+"/agents", map[string]any{"name": "Triage"}, nil, nil)
	mustCode(t, "a member deploys", code, ec, 403, "forbidden")
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Triage"}, &ag, nil)
	mustCode(t, "an editor deploys", code, ec, 201, "")
	if ag.TeamSlug != "tools" || ag.TemplateSlug != "assistant" || ag.Running {
		t.Fatalf("deployed %+v", ag)
	}
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "triage"}, nil, nil)
	mustCode(t, "the same name", code, ec, 409, "name_taken")
	base := "/v1/agents/" + ag.Id.String()

	// Any member tests it: the message is accepted, the reply arrives in the thread.
	code, ec = e.member.call("POST", base+"/threads/test-1/messages", map[string]any{"text": "hello", "requestId": "req-1"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	view := waitReply(t, e.member, base+"/threads/test-1", 2)
	if !strings.Contains(text(view.Messages[1]), "message 1 in this chat") {
		t.Fatalf("unexpected reply: %+v", view.Messages)
	}
	var threads []apitypes.AgentThread
	if code := e.member.get(base+"/threads", &threads); code != 200 || len(threads) != 1 || threads[0].Title != "hello" {
		t.Fatalf("threads = %d %+v", code, threads)
	}
	if code, body := e.member.raw("GET", base+"/threads/test-1/events?once=true", nil, nil); code != 200 || !strings.HasPrefix(string(body), "event: view\ndata: {") {
		t.Fatalf("events = %d %.200s", code, body)
	}
	var list []apitypes.Agent
	if code := e.member.get("/v1/agents", &list); code != 200 || len(list) != 1 || list[0].Id != ag.Id || !list[0].Running {
		t.Fatalf("member's agents = %d %+v", code, list)
	}

	// Private to the team: another team's people and platform staff don't reach it.
	for _, s := range []*session{e.outsider, e.admin, e.auditor} {
		code, ec := s.call("GET", base, nil, nil, nil)
		mustCode(t, "an outsider reads the agent", code, ec, 404, "agent_not_found")
		code, ec = s.call("POST", base+"/threads/test-1/messages", map[string]any{"text": "hi"}, nil, nil)
		mustCode(t, "an outsider talks to the agent", code, ec, 404, "agent_not_found")
	}

	// Hibernate, then wake on the next request with the conversation intact.
	code, ec = e.member.call("POST", base+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate", code, ec, 200, "")
	var got apitypes.Agent
	if e.member.get(base, &got); got.Running {
		t.Fatal("still running after hibernate")
	}
	code, ec = e.member.call("POST", base+"/threads/test-1/messages", map[string]any{"text": "again"}, nil, nil)
	mustCode(t, "send after hibernate", code, ec, 202, "")
	view = waitReply(t, e.member, base+"/threads/test-1", 4)
	if !strings.Contains(text(view.Messages[3]), "message 2 in this chat") {
		t.Fatalf("conversation not resumed: %+v", view.Messages)
	}

	// Turned off: refused. Deleted by a platform admin (without reading it): gone.
	code, ec = e.editor.call("PATCH", base, map[string]any{"enabled": false}, &got, ifMatch(got.Revision))
	mustCode(t, "turn off", code, ec, 200, "")
	code, ec = e.member.call("POST", base+"/threads/test-1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "send to a turned-off agent", code, ec, 409, "agent_disabled")
	code, ec = e.editor.call("DELETE", base, nil, nil, nil)
	mustCode(t, "an editor deletes", code, ec, 403, "forbidden")
	code, ec = e.admin.call("DELETE", base, nil, nil, nil)
	mustCode(t, "a platform admin deletes", code, ec, 200, "")
	code, ec = e.member.call("GET", base, nil, nil, nil)
	mustCode(t, "deleted", code, ec, 404, "agent_not_found")
}

// waitReply polls a thread until it is idle with at least n messages.
func waitReply(t *testing.T, s *session, path string, n int) apitypes.ChatView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var v apitypes.ChatView
		if code := s.get(path, &v); code == 200 && !v.Busy && len(v.Messages) >= n {
			return v
		}
		sleep100()
	}
	t.Fatalf("no reply in %s", path)
	return apitypes.ChatView{}
}

func text(m apitypes.ChatMessage) string {
	if m.Text == nil {
		return ""
	}
	return *m.Text
}

func sleep100() { time.Sleep(100 * time.Millisecond) }
