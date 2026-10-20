package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/config"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// probeRuntime writes a custom agent built with the library (../../runtime)
// into a temporary runtime directory for the local launcher: it exposes the
// conversation document probe.board, and probe.big with a bound of 10 bytes
// it never fits. Its tool is named get_time, which the demo model calls when
// a message asks the time: it adds a card to the board and returns details.
func probeRuntime(t *testing.T) string {
	t.Helper()
	runtimeDir, err := filepath.Abs("../../runtime")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(runtimeDir, "node_modules"), filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "probe-agent", "private": true, "type": "module"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	main := `import { defineAgent, defineDoc, defineExtension, defineTool, runAgentProcess, Type } from ` + "\"" + filepath.Join(runtimeDir, "src/lib/index.ts") + "\"" + `;
const Board = defineDoc({ kind: "probe.board", version: 3, scope: "conversation", history: "latest", fork: "current", initial: () => ({ cards: [] }) });
const Big = defineDoc({ kind: "probe.big", version: 1, scope: "conversation", history: "latest", fork: "current", initial: () => ({ text: "more than ten bytes" }) });
const card = defineTool({
	name: "get_time",
	description: "Adds a card",
	parameters: Type.Object({}),
	replay: "safe",
	execute: async (_args, api, context) => {
		let total = 0;
		await api.commit(async (tx) => {
			const board = await tx.doc(Board, api.conversationId);
			if (!board.cards.includes(api.callId)) board.cards.push(api.callId);
			total = board.cards.length;
		}, context);
		return { output: [{ type: "text", text: total + " cards" }], details: { cards: total } };
	},
});
await runAgentProcess(defineAgent({
	name: "probe",
	documents: [Board, Big],
	expose: [Board, { document: Big, maxBytes: 10 }],
	extensions: [defineExtension({ name: "probe", tools: [card] })],
}));
`
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Custom state reaching apps: a tool's details on its result in the thread
// view, an exposed document read with exactly the access reading the thread
// needs (a private thread's only by its creator; platform admins only with a
// reason, audited), bounded, and announced in the thread's event streams.
func TestThreadDocuments(t *testing.T) {
	dir := probeRuntime(t)
	app := newTestAppWith(t, func(c *config.Config) { c.Agents.RuntimeDir = dir }, testutil.Logger(), nil, false)
	e := newTeamEnvOn(t, app)
	a, b, owner := e.member, e.editor, e.owner
	var ag apitypes.Agent
	code, ec := e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Board keeper"}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	base := "/v1/agents/" + ag.Id.String()

	// A's private thread: the tool's details are on its result, and the board is listed in the view.
	code, ec = a.call("POST", base+"/threads/p1/messages", map[string]any{"text": "what time is it?", "visibility": "private"}, nil, nil)
	mustCode(t, "private thread", code, ec, 202, "")
	view := waitReply(t, a, base+"/threads/p1", 4)
	var result *apitypes.ChatMessage
	for i := range view.Messages {
		if view.Messages[i].Role == "tool" {
			result = &view.Messages[i]
		}
	}
	if result == nil || !jsonEqual(result.Details, map[string]any{"cards": 1}) {
		t.Fatalf("tool result %+v", result)
	}
	if view.Documents == nil || (*view.Documents)["probe.board"].Revision == "" {
		t.Fatalf("view documents %+v", view.Documents)
	}
	var doc apitypes.ThreadDocument
	code, ec = a.call("GET", base+"/threads/p1/documents/probe.board", nil, &doc, nil)
	mustCode(t, "A reads the board", code, ec, 200, "")
	if doc.Kind != "probe.board" || doc.Version != 3 || doc.Revision != (*view.Documents)["probe.board"].Revision {
		t.Fatalf("document %+v", doc)
	}
	if cards, _ := doc.Value["cards"].([]any); len(cards) != 1 {
		t.Fatalf("board %+v", doc.Value)
	}

	// Nobody else reaches a private thread's document; other teams don't see the agent.
	for who, s := range map[string]*session{"member B": b, "the owner": owner} {
		code, ec = s.call("GET", base+"/threads/p1/documents/probe.board", nil, nil, nil)
		mustCode(t, who+" reads A's private board", code, ec, 404, "thread_not_found")
	}
	code, ec = e.outsider.call("GET", base+"/threads/p1/documents/probe.board", nil, nil, nil)
	mustCode(t, "another team reads the board", code, ec, 404, "agent_not_found")
	code, ec = e.admin.call("GET", base+"/threads/p1/documents/probe.board", nil, nil, nil)
	mustCode(t, "a platform admin reads directly", code, ec, 404, "agent_not_found")

	// Kinds the agent doesn't expose, malformed kinds, and documents over their bound.
	for _, kind := range []string{"probe.hidden", "pi.live", "pdp.threads"} {
		code, ec = a.call("GET", base+"/threads/p1/documents/"+kind, nil, nil, nil)
		mustCode(t, "an unexposed "+kind, code, ec, 404, "document_not_found")
	}
	code, ec = a.call("GET", base+"/threads/p1/documents/.hidden", nil, nil, nil)
	mustCode(t, "a malformed kind", code, ec, 400, "invalid_document_kind")
	code, ec = a.call("GET", base+"/threads/p1/documents/probe.big", nil, nil, nil)
	mustCode(t, "a document over its bound", code, ec, 422, "document_too_large")
	code, ec = a.call("GET", base+"/threads/nope/documents/probe.board", nil, nil, nil)
	mustCode(t, "a thread the agent doesn't have", code, ec, 404, "thread_not_found")

	// Platform admins: only with a reason, recorded as a read of the conversation.
	readPath := "/v1/admin/agents/" + ag.Id.String() + "/threads/p1/documents/probe.board/read"
	code, ec = e.admin.call("POST", readPath, map[string]any{"reason": "short"}, nil, nil)
	mustCode(t, "a read with a short reason", code, ec, 400, "invalid_reason")
	code, ec = e.auditor.call("POST", readPath, map[string]any{"reason": "Looking into a complaint"}, nil, nil)
	mustCode(t, "an auditor reads", code, ec, 403, "forbidden")
	var read apitypes.ThreadDocument
	code, ec = e.admin.call("POST", readPath, map[string]any{"reason": "Abuse report #18 names this board"}, &read, nil)
	mustCode(t, "an admin reads with a reason", code, ec, 200, "")
	if read.Revision != doc.Revision {
		t.Fatalf("admin read %+v", read)
	}
	var log apitypes.AuditPage
	if code := owner.get(toolsTeam+"/audit?action=thread.admin_read", &log); code != 200 || len(log.Items) != 1 ||
		log.Items[0].Metadata["document"] != "probe.board" || log.Items[0].Metadata["reason"] != "Abuse report #18 names this board" ||
		log.Items[0].TargetId != ag.Id.String()+"/p1" {
		t.Fatalf("team audit %d %+v", code, log)
	}
	code, ec = e.admin.call("POST", "/v1/admin/agents/"+ag.Id.String()+"/threads/p1/documents/probe.hidden/read", map[string]any{"reason": "Checking another document"}, nil, nil)
	mustCode(t, "an admin reads an unexposed document", code, ec, 404, "document_not_found")
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'thread.admin_read'`); n != "1" {
		t.Fatalf("admin reads recorded: %s", n)
	}

	// A shared thread: a change to the board is announced in both stream formats.
	code, ec = b.call("POST", base+"/threads/s1/messages", map[string]any{"text": "hello"}, nil, nil)
	mustCode(t, "shared thread", code, ec, 202, "")
	waitReply(t, b, base+"/threads/s1", 2)
	var first apitypes.ThreadDocument
	code, ec = a.call("GET", base+"/threads/s1/documents/probe.board", nil, &first, nil)
	mustCode(t, "another member reads a shared board", code, ec, 200, "")
	deltas := followStream(t, a, base+"/threads/s1/events?format=delta")
	views := followStream(t, b, base+"/threads/s1/events")
	deltas.wait(t, "snapshot", "")
	code, ec = b.call("POST", base+"/threads/s1/messages", map[string]any{"text": "and the time?"}, nil, nil)
	mustCode(t, "a message that changes the board", code, ec, 202, "")
	waitReply(t, b, base+"/threads/s1", 6)
	var second apitypes.ThreadDocument
	a.call("GET", base+"/threads/s1/documents/probe.board", nil, &second, nil)
	if second.Revision == first.Revision {
		t.Fatalf("the board's revision didn't change: %s", second.Revision)
	}
	deltas.wait(t, "document.updated", `"revision":"`+second.Revision+`"`)
	views.wait(t, "view", `"probe.board":{"revision":"`+second.Revision+`"}`)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// sseFollower collects a server-sent event stream's events.
type sseFollower struct {
	events chan [2]string // event name, data
	seen   [][2]string
}

func followStream(t *testing.T, s *session, path string) *sseFollower {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", s.app.URL+path, nil)
	res, err := s.client.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream %s: %v %v", path, err, res)
	}
	t.Cleanup(func() { res.Body.Close() })
	f := &sseFollower{events: make(chan [2]string, 1024)}
	go func() {
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 4<<20)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				f.events <- [2]string{event, v}
			}
		}
	}()
	return f
}

// wait waits for an event with the name whose data contains want.
func (f *sseFollower) wait(t *testing.T, name, want string) {
	t.Helper()
	for _, ev := range f.seen {
		if ev[0] == name && strings.Contains(ev[1], want) {
			return
		}
	}
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-f.events:
			f.seen = append(f.seen, ev)
			if ev[0] == name && strings.Contains(ev[1], want) {
				return
			}
		case <-deadline:
			t.Fatalf("no %s event with %s; saw %d events", name, want, len(f.seen))
		}
	}
}
