package httpapi_test

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// threadPage is a thread list reply with its optional cursor.
type threadPage struct {
	Data       []apitypes.AgentThread `json:"data"`
	NextCursor *string                `json:"nextCursor"`
}

func listThreads(t *testing.T, s *session, path string) (threadPage, string) {
	t.Helper()
	code, raw := s.raw("GET", path, nil, nil)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", path, code, raw)
	}
	var page threadPage
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	return page, string(raw)
}

func threadOrder(ts []apitypes.AgentThread) []string {
	out := make([]string, len(ts))
	for i, th := range ts {
		out[i] = th.ThreadId
	}
	return out
}

// Thread lists page and search by title without changing the unpaged reply,
// and keep others' private threads out of every page.
func TestAgentThreadPaging(t *testing.T) {
	e := newTeamEnv(t)
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Paging"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()

	titles := []string{"Alpha one", "beta two", "Gamma 50%_off", "ALPHA three", "delta", "epsilon_x", "Zeta \\ back"}
	for _, title := range titles {
		code, ec := e.member.call("POST", base+"/threads", map[string]any{"title": title}, nil, nil)
		mustCode(t, "create "+title, code, ec, 201, "")
	}
	code, ec = e.editor.call("POST", base+"/threads", map[string]any{"title": "alpha private", "visibility": "private"}, nil, nil)
	mustCode(t, "create private", code, ec, 201, "")
	// As if each had its first message, at distinct times, two at the same instant (the ID breaks the tie).
	e.scalar(`WITH ranked AS (SELECT thread_id, row_number() OVER (ORDER BY title) AS n FROM agent_threads WHERE agent_id = $1)
		UPDATE agent_threads t SET reserved = false,
		  updated_at = CASE WHEN r.n IN (2, 3) THEN timestamptz '2026-01-01 00:00:00.123456Z'
		                    ELSE timestamptz '2026-01-01' + r.n * interval '1 minute' END
		FROM ranked r WHERE t.agent_id = $1 AND t.thread_id = r.thread_id RETURNING 'ok'`, ag.Id)

	// Without limit: exactly the whole list, no cursor.
	all, raw := listThreads(t, e.member, base+"/threads")
	if len(all.Data) != len(titles) || strings.Contains(raw, "nextCursor") || !strings.HasPrefix(raw, `{"data":[`) {
		t.Fatalf("unpaged list %s", raw)
	}
	for i := 1; i < len(all.Data); i++ {
		a, b := all.Data[i-1], all.Data[i]
		if a.UpdatedAt < b.UpdatedAt || (a.UpdatedAt == b.UpdatedAt && a.ThreadId > b.ThreadId) {
			t.Fatalf("order %v", threadOrder(all.Data))
		}
	}

	// Pages of 3 give the same threads in the same order, the last without a cursor.
	var paged []apitypes.AgentThread
	path := base + "/threads?limit=3"
	for pages := 0; ; pages++ {
		page, _ := listThreads(t, e.member, path)
		if len(page.Data) > 3 || pages > 5 {
			t.Fatalf("page %d: %d threads", pages, len(page.Data))
		}
		paged = append(paged, page.Data...)
		if page.NextCursor == nil {
			break
		}
		path = base + "/threads?limit=3&cursor=" + url.QueryEscape(*page.NextCursor)
	}
	if !slices.Equal(threadOrder(paged), threadOrder(all.Data)) {
		t.Fatalf("paged %v, want %v", threadOrder(paged), threadOrder(all.Data))
	}
	if page, _ := listThreads(t, e.member, base+"/threads?limit=7"); page.NextCursor != nil || len(page.Data) != 7 {
		t.Fatalf("a page holding everything: %d, %v", len(page.Data), page.NextCursor)
	}

	// Title search: case-insensitive, LIKE wildcards are literal, others' private threads never match.
	search := func(s *session, q string, extra string) []string {
		page, _ := listThreads(t, s, base+"/threads?q="+url.QueryEscape(q)+extra)
		out := []string{}
		for _, th := range page.Data {
			out = append(out, th.Title)
		}
		slices.Sort(out)
		return out
	}
	for q, want := range map[string][]string{
		"alpha": {"ALPHA three", "Alpha one"},
		"50%_":  {"Gamma 50%_off"},
		"%":     {"Gamma 50%_off"},
		"_":     {"Gamma 50%_off", "epsilon_x"},
		"\\":    {"Zeta \\ back"},
		"nope":  {},
	} {
		if got := search(e.member, q, ""); !slices.Equal(got, want) {
			t.Fatalf("q=%q: %v, want %v", q, got, want)
		}
	}
	if got := search(e.editor, "ALPHA", ""); !slices.Equal(got, []string{"ALPHA three", "Alpha one", "alpha private"}) {
		t.Fatalf("the creator's search %v", got)
	}
	page, _ := listThreads(t, e.member, base+"/threads?q=alpha&limit=1")
	if len(page.Data) != 1 || page.NextCursor == nil {
		t.Fatalf("searched page %+v", page)
	}
	next, _ := listThreads(t, e.member, base+"/threads?q=alpha&limit=1&cursor="+url.QueryEscape(*page.NextCursor))
	if len(next.Data) != 1 || next.NextCursor != nil || next.Data[0].ThreadId == page.Data[0].ThreadId {
		t.Fatalf("second searched page %+v", next)
	}

	// Malformed parameters.
	forged := base64.RawURLEncoding.EncodeToString([]byte(`["soon","t1"]`))
	for query, want := range map[string]string{
		"limit=0":                       "invalid_limit",
		"limit=201":                     "invalid_limit",
		"limit=x":                       "invalid_limit",
		"cursor=%21%21":                 "invalid_cursor",
		"cursor=" + forged:              "invalid_cursor",
		"q=" + strings.Repeat("a", 201): "invalid_search",
	} {
		code, ec := e.member.call("GET", base+"/threads?"+query, nil, nil, nil)
		mustCode(t, query, code, ec, 400, want)
	}
	// Another team's people can't list, paged or not.
	code, ec = e.outsider.call("GET", base+"/threads?limit=2", nil, nil, nil)
	mustCode(t, "an outsider pages", code, ec, 404, "agent_not_found")

	// whenBusy is followUp or steer.
	code, ec = e.member.call("POST", base+"/threads/t9/messages", map[string]any{"text": "hi", "whenBusy": "now"}, nil, nil)
	mustCode(t, "whenBusy", code, ec, 400, "invalid_when_busy")
}

// Through the platform: a conversation pages backwards, the delta stream
// starts with a snapshot numbered after Last-Event-ID, and a steering message
// is accepted (the runtime's tests cover how it is applied).
func TestAgentConversationPagingAndDeltas(t *testing.T) {
	e := newTeamEnv(t)
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Pages"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()
	code, ec = e.member.call("POST", base+"/threads/t1/messages", map[string]any{"text": "hello", "whenBusy": "steer"}, nil, nil)
	mustCode(t, "send", code, ec, 202, "")
	waitThread(t, e.member, base, "t1", func(th apitypes.AgentThread) bool { return !th.Busy && th.LastReplyAt != nil })

	var whole, last, earlier apitypes.ChatView
	if code := e.member.get(base+"/threads/t1", &whole); code != 200 || len(whole.Messages) < 2 || whole.EarlierCursor != nil {
		t.Fatalf("whole view %d %+v", code, whole)
	}
	if code := e.member.get(base+"/threads/t1?limit=1", &last); code != 200 || len(last.Messages) != 1 || last.EarlierCursor == nil ||
		last.Messages[0].Id != whole.Messages[len(whole.Messages)-1].Id {
		t.Fatalf("last page %d %+v", code, last)
	}
	if code := e.member.get(base+"/threads/t1?limit=1&before="+url.QueryEscape(*last.EarlierCursor), &earlier); code != 200 || len(earlier.Messages) != 1 ||
		earlier.Messages[0].Id != whole.Messages[len(whole.Messages)-2].Id {
		t.Fatalf("earlier page %d %+v", code, earlier)
	}
	code, ec = e.member.call("GET", base+"/threads/t1?before=nope", nil, nil, nil)
	mustCode(t, "a malformed before", code, ec, 400, "invalid_cursor")
	code, ec = e.member.call("GET", base+"/threads/t1/events?format=delta&v=2", nil, nil, nil)
	mustCode(t, "an unknown delta version", code, ec, 400, "unsupported_version")

	code, body := e.member.raw("GET", base+"/threads/t1/events?format=delta&once=true", nil, map[string]string{"Last-Event-ID": "41"})
	if code != 200 || !strings.HasPrefix(string(body), "id: 42\nevent: snapshot\ndata: ") {
		t.Fatalf("delta snapshot %d %.80s", code, body)
	}
	var snap struct {
		Type string            `json:"type"`
		Seq  int64             `json:"seq"`
		View apitypes.ChatView `json:"view"`
	}
	data, _ := strings.CutPrefix(strings.SplitN(string(body), "\n", 4)[2], "data: ")
	if err := json.Unmarshal([]byte(data), &snap); err != nil || snap.Type != "snapshot" || snap.Seq != 42 || len(snap.View.Messages) != len(whole.Messages) {
		t.Fatalf("snapshot %v %+v", err, snap)
	}
	// The whole-view stream is unchanged.
	code, body = e.member.raw("GET", base+"/threads/t1/events?once=true", nil, nil)
	if code != 200 || !strings.HasPrefix(string(body), "event: view\ndata: {\"busy\":false,\"messages\":[") {
		t.Fatalf("view stream %d %.80s", code, body)
	}
}
