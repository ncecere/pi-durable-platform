package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// openRawAppStream opens the app-level event stream and returns the raw
// data of its thread events, as the app receives them.
func openRawAppStream(t *testing.T, app *testApp, secret string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", app.URL+"/v1/app/events", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("open the app stream: %v %v", err, res)
	}
	out := make(chan string, 64)
	go func() {
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok && strings.Contains(data, "threadId") {
				out <- data
			}
		}
	}()
	return out
}

// The app-level event stream names people only by the app's subjects:
// never a platform user ID, an email or a name. A person who connected the
// app appears as their subject; anyone else (a person who didn't connect
// it, a service account) without any identifier.
func TestOAuthAppEventStreamKeepsPeoplePseudonymous(t *testing.T) {
	e := newTrustedAppEnv(t, 0)
	app, admin := e.app, e.admin
	ann, ben := app.signIn("user"), app.signIn("alex")
	createTeam(t, admin, "shared", "user@localhost")
	c, ec := ann.call("POST", "/v1/teams/shared/members", map[string]any{"email": "alex@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add Ben", c, ec, 201, "")
	exec(t, app, `UPDATE users SET display_name = 'Ben Display' WHERE email = 'alex@localhost'`)
	exec(t, app, `UPDATE users SET display_name = 'Ann Display' WHERE email = 'user@localhost'`)
	subAnn := e.connect(t, ann, "a")["subject"].(string)

	var ag apitypes.Agent
	c, ec = ann.call("POST", "/v1/teams/shared/agents", map[string]any{"name": "Shared"}, &ag, nil)
	mustCode(t, "agent", c, ec, 201, "")
	stream := openRawAppStream(t, app, e.secret)

	// Ben (who never connected the app) starts a shared thread; Ann and Ben have sent to it.
	var th apitypes.AgentThread
	c, ec = ben.call("POST", "/v1/agents/"+ag.Id.String()+"/threads", map[string]any{"visibility": "shared"}, &th, nil)
	mustCode(t, "Ben starts a thread", c, ec, 201, "")
	exec(t, app, `UPDATE agent_threads SET reserved = false WHERE agent_id = $1 AND thread_id = $2`, ag.Id, th.ThreadId)
	for i, u := range []*session{ann, ben} {
		exec(t, app, `INSERT INTO agent_thread_participants (agent_id, thread_id, user_id, first_sent_at, last_sent_at)
			VALUES ($1, $2, $3, now() + make_interval(secs => $4), now() + make_interval(secs => $4))`, ag.Id, th.ThreadId, u.me.User.Id, i)
	}
	c, ec = ann.call("PATCH", "/v1/agents/"+ag.Id.String()+"/threads/"+th.ThreadId, map[string]any{"title": "Plans"}, nil, nil)
	mustCode(t, "rename", c, ec, 200, "")

	var raw string
	deadline := time.After(5 * time.Second)
	for raw == "" {
		select {
		case data := <-stream:
			if strings.Contains(data, `"Plans"`) {
				raw = data
			}
		case <-deadline:
			t.Fatal("no event for the renamed thread")
		}
	}
	for what, v := range map[string]string{"Ann's user ID": ann.me.User.Id.String(), "Ben's user ID": ben.me.User.Id.String(),
		"Ann's email": "user@localhost", "Ben's email": "alex@localhost", "Ben's name": "Ben Display", "Ann's name": "Ann Display"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(v)) {
			t.Errorf("the app stream carries %s: %s", what, raw)
		}
	}
	var ev struct {
		Subjects []string `json:"subjects"`
		Thread   struct {
			CreatedBy    map[string]any   `json:"createdBy"`
			Participants []map[string]any `json:"participants"`
		} `json:"thread"`
	}
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if len(ev.Subjects) != 1 || ev.Subjects[0] != subAnn {
		t.Fatalf("subjects %v", ev.Subjects)
	}
	if cb := ev.Thread.CreatedBy; cb["kind"] != "user" || cb["subject"] != nil || len(cb) != 1 {
		t.Fatalf("createdBy of a person who didn't connect the app: %v", cb)
	}
	ps := ev.Thread.Participants
	if len(ps) != 2 || ps[0]["subject"] != subAnn || ps[0]["kind"] != "user" || ps[1]["subject"] != nil || ps[1]["kind"] != "user" ||
		ps[1]["lastSentAt"] == nil {
		t.Fatalf("participants %v", ps)
	}
}
