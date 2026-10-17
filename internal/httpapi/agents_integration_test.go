package httpapi_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// A person's assistant: created once, talked to through the platform (which
// starts its runtime), private to them, and survives hibernation.
func TestPersonalAssistant(t *testing.T) {
	app := newTestApp(t, nil)
	alex := app.signIn("alex")

	var mine apitypes.Agent
	code, _ := alex.call("POST", "/v1/me/assistant", nil, &mine, nil)
	mustCode(t, "create assistant", code, "", 201, "")
	var again apitypes.Agent
	code, _ = alex.call("POST", "/v1/me/assistant", nil, &again, nil)
	mustCode(t, "existing assistant", code, "", 200, "")
	if again.Id != mine.Id || mine.Kind != "personal" {
		t.Fatalf("assistant changed: %v %v", mine, again)
	}
	base := "/v1/agents/" + mine.Id.String()

	// Talk to it: the message is accepted, the reply arrives in the thread.
	var accepted apitypes.AgentMessageAccepted
	code, errCode := alex.call("POST", base+"/threads/chat-1/messages", map[string]any{"text": "hello", "requestId": "req-1"}, &accepted, nil)
	mustCode(t, "send", code, errCode, 202, "")
	view := waitReply(t, alex, base+"/threads/chat-1", 2)
	if !strings.Contains(text(view.Messages[1]), "message 1 in this chat") {
		t.Fatalf("unexpected reply: %+v", view.Messages)
	}
	var threads []apitypes.AgentThread
	if code := alex.get(base+"/threads", &threads); code != 200 || len(threads) != 1 || threads[0].Title != "hello" {
		t.Fatalf("threads = %d %+v", code, threads)
	}
	if code, body := alex.raw("GET", base+"/threads/chat-1/events?once=true", nil, nil); code != 200 || !strings.HasPrefix(string(body), "event: view\ndata: {") {
		t.Fatalf("events = %d %.200s", code, body)
	}

	// Private: another person and platform admins don't see it.
	blair := app.signIn("blair")
	for _, s := range []*session{blair, app.signIn("admin")} {
		code, errCode := s.call("GET", base, nil, nil, nil)
		mustCode(t, "someone else reads someone's assistant", code, errCode, 404, "agent_not_found")
		code, errCode = s.call("POST", base+"/threads/chat-1/messages", map[string]any{"text": "hi"}, nil, nil)
		mustCode(t, "someone else talks to someone's assistant", code, errCode, 404, "agent_not_found")
	}
	var blairs []apitypes.Agent
	if code := blair.get("/v1/agents", &blairs); code != 200 || len(blairs) != 0 {
		t.Fatalf("blair's agents = %d %+v", code, blairs)
	}

	// Hibernate, then wake on the next request with the conversation intact.
	code, errCode = alex.call("POST", base+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate", code, errCode, 200, "")
	var got apitypes.Agent
	if alex.get(base, &got); got.Running {
		t.Fatal("still running after hibernate")
	}
	code, errCode = alex.call("POST", base+"/threads/chat-1/messages", map[string]any{"text": "again"}, nil, nil)
	mustCode(t, "send after hibernate", code, errCode, 202, "")
	view = waitReply(t, alex, base+"/threads/chat-1", 4)
	if !strings.Contains(text(view.Messages[3]), "message 2 in this chat") {
		t.Fatalf("conversation not resumed: %+v", view.Messages)
	}

	// Turned off: refused. Deleted by a platform admin: gone.
	code, errCode = alex.call("PATCH", base, map[string]any{"enabled": false}, &got, ifMatch(got.Revision))
	mustCode(t, "turn off", code, errCode, 200, "")
	code, errCode = alex.call("POST", base+"/threads/chat-1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "send to a turned-off agent", code, errCode, 409, "agent_disabled")
	code, errCode = app.signIn("admin").call("DELETE", base, nil, nil, nil)
	mustCode(t, "admin deletes", code, errCode, 200, "")
	code, errCode = alex.call("GET", base, nil, nil, nil)
	mustCode(t, "deleted", code, errCode, 404, "agent_not_found")
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
		time.Sleep(100 * time.Millisecond)
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
