package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// liveStream is an open server-sent event stream: its lines, and done when
// the server ended it.
type liveStream struct {
	lines chan string
	done  chan struct{}
}

func openStream(t *testing.T, s *session, path string) *liveStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", s.app.URL+path, nil)
	res, err := s.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("open %s: %v %v", path, err, res)
	}
	l := &liveStream{lines: make(chan string, 256), done: make(chan struct{})}
	go func() {
		defer close(l.done)
		defer res.Body.Close()
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			select {
			case l.lines <- sc.Text():
			default:
			}
		}
	}()
	return l
}

// staysOpen: the server doesn't end the stream for d.
func (l *liveStream) staysOpen(d time.Duration) bool {
	select {
	case <-l.done:
		return false
	case <-time.After(d):
		return true
	}
}

// endsWithin: the server ends the stream within d.
func (l *liveStream) endsWithin(d time.Duration) bool {
	select {
	case <-l.done:
		return true
	case <-time.After(d):
		return false
	}
}

// heard: a line containing want arrives within d.
func (l *liveStream) heard(want string, d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case line := <-l.lines:
			if strings.Contains(line, want) {
				return true
			}
		case <-deadline:
			return false
		case <-l.done:
			return false
		}
	}
}

// An open event stream is decided when it opens, and checked again every
// interval: a person denied by the group re-check, suspended or removed from
// the team stops receiving (thread titles and participant names, or a
// conversation) within the interval, while everyone else's streams carry on.
func TestOpenEventStreamsEndWhenAccessEnds(t *testing.T) {
	app := newTestApp(t, nil)
	const every = 200 * time.Millisecond
	app.Agents.SetStreamRecheck(every)
	e := newTeamEnvOn(t, app)
	code, ec := e.owner.call("POST", toolsTeam+"/members", map[string]any{"email": "casey@localhost", "role": "member"}, nil, nil)
	mustCode(t, "add casey", code, ec, 201, "")

	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Streams"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	code, ec = e.owner.call("POST", base+"/threads/t1/messages", map[string]any{"text": "Quarterly plan"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	waitReply(t, e.owner, base+"/threads/t1", 2)

	// Everyone opens the three kinds of stream: all agents, one agent, one thread's conversation.
	type streams struct{ all, one, thread *liveStream }
	open := func(s *session) streams {
		return streams{openStream(t, s, "/v1/agents/events"), openStream(t, s, "/v1/agents/events?agentId="+ag.Id.String()),
			openStream(t, s, base+"/threads/t1/events")}
	}
	denied, suspended, removed, kept := open(e.member), open(e.editor), open(e.outsider), open(e.owner)
	for _, s := range []streams{denied, suspended, removed, kept} {
		for _, l := range []*liveStream{s.all, s.one, s.thread} {
			if !l.staysOpen(3 * every) {
				t.Fatal("a stream of someone with access ended")
			}
		}
	}

	userOf := func(email string) string { return scalar(t, app, `SELECT id::text FROM users WHERE email = $1`, email) }
	// Denied by the group re-check (the person, their sessions still there).
	exec(t, app, `UPDATE users SET group_denied_at = now(), group_denied_reason = 'not_in_group' WHERE email = 'blair@localhost'`)
	// Suspended by an admin.
	var detail apitypes.UserDetail
	e.admin.get("/v1/admin/users/"+userOf("alex@localhost"), &detail)
	code, ec = e.admin.call("PATCH", "/v1/admin/users/"+detail.User.Id.String(), map[string]any{"status": "suspended"}, nil, ifMatch(detail.User.Revision))
	mustCode(t, "suspend", code, ec, 200, "")
	// Removed from the team (casey is a member of two teams: this one only).
	exec(t, app, `DELETE FROM team_members WHERE user_id = $1 AND team_id = (SELECT id FROM teams WHERE slug = 'tools')`, userOf("casey@localhost"))

	for who, s := range map[string]streams{"denied": denied, "suspended": suspended} {
		for name, l := range map[string]*liveStream{"all agents": s.all, "one agent": s.one, "thread": s.thread} {
			if !l.endsWithin(10 * every) {
				t.Errorf("%s: the %s stream stayed open", who, name)
			}
		}
	}
	// Removed from the team: the stream of the agent, and the conversation, end; the stream of
	// all their agents stays (they may use others) but stops carrying this agent's threads.
	for name, l := range map[string]*liveStream{"one agent": removed.one, "thread": removed.thread} {
		if !l.endsWithin(10 * every) {
			t.Errorf("removed: the %s stream stayed open", name)
		}
	}
	code, ec = e.owner.call("POST", base+"/threads/t2/messages", map[string]any{"text": "Salary review"}, nil, nil)
	mustCode(t, "send t2", code, ec, 202, "")
	if !kept.all.heard(`"threadId":"t2"`, 10*time.Second) || !kept.one.heard(`"threadId":"t2"`, 10*time.Second) {
		t.Fatal("the owner's streams stopped carrying events")
	}
	if removed.all.heard(`"threadId":"t2"`, 8*every) {
		t.Fatal("a person removed from the team still receives its agent's thread events on their all-agents stream")
	}
	// Those with access are unaffected, after many intervals.
	for _, l := range []*liveStream{kept.all, kept.one, kept.thread} {
		if !l.staysOpen(5 * every) {
			t.Fatal("a stream of someone with access was ended")
		}
	}
	// And a person denied can't open a new one.
	if code, _ := e.member.raw("GET", "/v1/agents/events?once=true", nil, nil); code != 401 && code != 403 {
		t.Fatalf("a denied person opens a stream: %d", code)
	}
}
