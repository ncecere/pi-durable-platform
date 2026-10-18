package httpapi_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// waitThread polls the platform's thread list until want accepts the thread.
func waitThread(t *testing.T, s *session, agentPath, threadID string, want func(apitypes.AgentThread) bool) apitypes.AgentThread {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last []apitypes.AgentThread
	for time.Now().Before(deadline) {
		last = nil
		if code := s.get(agentPath+"/threads", &last); code == 200 {
			for _, th := range last {
				if th.ThreadId == threadID && want(th) {
					return th
				}
			}
		}
		sleep100()
	}
	t.Fatalf("thread %s never matched: %+v", threadID, last)
	return apitypes.AgentThread{}
}

// The platform's thread record: status without waking the agent, an event
// stream across agents, renaming, deleting, a failed reply, and threads
// that existed before the record are reported when the agent starts.
func TestAgentThreads(t *testing.T) {
	e := newTeamEnv(t)
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Threads"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()

	// One event stream for every agent the member may use.
	req, _ := http.NewRequest("GET", e.app.URL+"/v1/agents/events", nil)
	res, err := e.member.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, res)
	}
	defer res.Body.Close()
	events := make(chan apitypes.AgentThreadEvent, 32)
	go func() {
		sc := bufio.NewScanner(res.Body)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok && event == "thread" {
				var ev apitypes.AgentThreadEvent
				if json.Unmarshal([]byte(v), &ev) == nil {
					events <- ev
				}
			}
		}
	}()

	// A message: the thread is recorded, working, then finished.
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "First line\nmore"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	done := waitThread(t, e.member, base, "t1", func(th apitypes.AgentThread) bool { return !th.Busy && th.LastReplyAt != nil })
	if done.Title != "First line" || done.LastError != nil || done.UpdatedAt < *done.LastReplyAt {
		t.Fatalf("finished thread %+v", done)
	}
	sawBusy, sawDone := false, false
	deadline := time.After(10 * time.Second)
	for !(sawBusy && sawDone) {
		select {
		case ev := <-events:
			if ev.AgentId == ag.Id && ev.ThreadId == "t1" && ev.Thread != nil {
				sawBusy = sawBusy || ev.Thread.Busy
				sawDone = sawDone || (!ev.Thread.Busy && ev.Thread.LastReplyAt != nil)
			}
		case <-deadline:
			t.Fatalf("events: busy %v, done %v", sawBusy, sawDone)
		}
	}

	// Rename; another team's people can't.
	var renamed apitypes.AgentThread
	code, ec = e.member.call("PATCH", base+"/threads/t1", map[string]any{"title": "Renamed"}, &renamed, nil)
	mustCode(t, "rename", code, ec, 200, "")
	if renamed.Title != "Renamed" {
		t.Fatalf("renamed %+v", renamed)
	}
	code, ec = e.outsider.call("PATCH", base+"/threads/t1", map[string]any{"title": "Mine"}, nil, nil)
	mustCode(t, "an outsider renames", code, ec, 404, "agent_not_found")
	code, ec = e.member.call("PATCH", base+"/threads/nope", map[string]any{"title": "x"}, nil, nil)
	mustCode(t, "rename a thread that doesn't exist", code, ec, 404, "thread_not_found")

	// Listing doesn't wake a hibernated agent.
	e.member.call("POST", base+"/hibernate", nil, nil, nil)
	var threads []apitypes.AgentThread
	if code := e.member.get(base+"/threads", &threads); code != 200 || len(threads) != 1 || threads[0].Title != "Renamed" {
		t.Fatalf("threads %d %+v", code, threads)
	}
	var got apitypes.Agent
	if e.member.get(base, &got); got.Running {
		t.Fatal("listing threads woke the agent")
	}

	// A thread the record lost is reported again when the agent starts.
	e.scalar(`DELETE FROM agent_threads WHERE agent_id = $1 RETURNING thread_id`, ag.Id)
	code, ec = e.member.call("POST", base+"/threads/t2/messages", map[string]any{"text": "second"}, nil, nil)
	mustCode(t, "send to wake", code, ec, 202, "")
	waitThread(t, e.member, base, "t1", func(apitypes.AgentThread) bool { return true })
	waitThread(t, e.member, base, "t2", func(th apitypes.AgentThread) bool { return !th.Busy })

	// Delete: gone from the record, and announced.
	code, ec = e.member.call("DELETE", base+"/threads/t2", nil, nil, nil)
	mustCode(t, "delete", code, ec, 200, "")
	e.member.get(base+"/threads", &threads)
	if len(threads) != 1 || threads[0].ThreadId != "t1" {
		t.Fatalf("after delete %+v", threads)
	}
	deadline = time.After(10 * time.Second)
	for deleted := false; !deleted; {
		select {
		case ev := <-events:
			deleted = ev.ThreadId == "t2" && ev.Deleted
		case <-deadline:
			t.Fatal("no delete event")
		}
	}

	// An outsider's stream hears nothing of this agent, and can't ask for it.
	code, ec = e.outsider.call("GET", "/v1/agents/events?agentId="+ag.Id.String()+"&once=true", nil, nil, nil)
	mustCode(t, "an outsider streams the agent", code, ec, 404, "agent_not_found")
	if code, body := e.outsider.raw("GET", "/v1/agents/events?once=true", nil, nil); code != 200 || !strings.HasPrefix(string(body), "event: ready") {
		t.Fatalf("outsider stream %d %s", code, body)
	}
}

// A reply that fails records its error.
func TestAgentThreadFailure(t *testing.T) {
	e := newTeamEnv(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"upstream_rejected","message":"The provider rejected the request"}}`)
	}))
	t.Cleanup(failing.Close)
	var prov apitypes.ModelProvider
	code, ec := e.admin.call("POST", "/v1/admin/model-providers", map[string]any{"name": "Failing", "kind": "openai-compatible",
		"baseUrl": failing.URL + "/v1", "apiKey": "k"}, &prov, nil)
	mustCode(t, "provider", code, ec, 201, "")
	var model apitypes.Model
	code, ec = e.admin.call("POST", "/v1/admin/model-providers/"+prov.Id.String()+"/models", map[string]any{"upstreamId": "bad"}, &model, nil)
	mustCode(t, "model", code, ec, 201, "")
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Failing", "modelId": model.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "hi"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	th := waitThread(t, e.member, base, "t1", func(th apitypes.AgentThread) bool { return !th.Busy && th.LastReplyAt != nil })
	if th.LastError == nil || !strings.Contains(*th.LastError, "upstream_rejected") {
		t.Fatalf("failed thread %+v", th)
	}
}
