package httpapi_test

import (
	"testing"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

// A client-chosen thread ID someone set MCP servers on before anyone sent a
// message is a reserved thread: hidden from everyone but its creator. Its
// servers are the creator's choice, so they must not become active for
// another person's first message to that ID (or the creator's, if someone else
// had set them).
func TestReservedThreadServersAreTheCreatorsOnly(t *testing.T) {
	e := newThreadClaimEnv(t)
	a, b := e.member, e.editor // A sends the first message; B pre-seeds the ID
	bID, aID := b.me.User.Id.String(), a.me.User.Id.String()

	// B sets servers on an ID nobody has used: a reserved, shared thread of B's.
	code, ec := b.call("PUT", e.base+"/threads/guess1/mcp-servers", mcpServersBody("", "Evil"), nil, nil)
	mustCode(t, "B pre-seeds the ID", code, ec, 200, "")
	if creator, _, reserved := e.record(e.agent, "guess1"); creator != bID || reserved != "true" {
		t.Fatalf("record: creator %q reserved %q", creator, reserved)
	}
	// A's first message to that ID goes through, and B's servers don't come with it.
	code, ec = a.call("POST", e.base+"/threads/guess1/messages", map[string]any{"text": "My own chat"}, nil, nil)
	mustCode(t, "A's first message", code, ec, 202, "")
	if got := e.serverCount(e.agent, "guess1"); got != "0" {
		t.Fatalf("B's servers are still on the thread after A's first message: %s", got)
	}
	waitReply(t, a, e.base+"/threads/guess1", 2)
	if _, _, reserved := e.record(e.agent, "guess1"); reserved != "false" {
		t.Fatalf("the reservation did not end: %s", reserved)
	}
	var list []apitypes.ThreadMcpServer
	if code, raw := a.raw("GET", e.base+"/threads/guess1/mcp-servers", nil, nil); code != 200 {
		t.Fatalf("servers: %d %s", code, raw)
	} else {
		mustDecode(t, raw, &list)
		if len(list) != 0 {
			t.Fatalf("servers listed: %+v", list)
		}
	}

	// The sender's own servers in that message are the ones that apply.
	code, ec = b.call("PUT", e.base+"/threads/guess2/mcp-servers", mcpServersBody("", "Evil"), nil, nil)
	mustCode(t, "B pre-seeds a second ID", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads/guess2/messages", map[string]any{"text": "With mine",
		"mcpServers": mcpServersBody("", "Mine")["servers"]}, nil, nil)
	mustCode(t, "A's first message with servers", code, ec, 202, "")
	if names := e.scalar(`SELECT string_agg(name, ',') FROM thread_mcp_servers WHERE agent_id = $1 AND thread_id = 'guess2'`, e.agent); names != "Mine" {
		t.Fatalf("servers after A's message: %q", names)
	}

	// A refused message (a visibility the thread doesn't have) changes nothing: the thread stays B's, hidden, with its servers.
	code, ec = b.call("PUT", e.base+"/threads/guess3/mcp-servers", mcpServersBody("", "Evil"), nil, nil)
	mustCode(t, "B pre-seeds a third ID", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads/guess3/messages", map[string]any{"text": "Private please", "visibility": "private"}, nil, nil)
	mustCode(t, "A asks for a private thread on B's ID", code, ec, 409, "visibility_fixed")
	if got := e.serverCount(e.agent, "guess3"); got != "1" {
		t.Fatalf("a refused message dropped the servers: %s", got)
	}
	if _, _, reserved := e.record(e.agent, "guess3"); reserved != "true" {
		t.Fatal("a refused message ended the reservation")
	}

	// The creator's own first message keeps their servers.
	code, ec = a.call("PUT", e.base+"/threads/mine1/mcp-servers", mcpServersBody("", "Tools"), nil, nil)
	mustCode(t, "A sets servers on their own ID", code, ec, 200, "")
	code, ec = a.call("POST", e.base+"/threads/mine1/messages", map[string]any{"text": "Use them"}, nil, nil)
	mustCode(t, "A's own first message", code, ec, 202, "")
	if got := e.serverCount(e.agent, "mine1"); got != "1" {
		t.Fatalf("the creator's servers after their first message: %s", got)
	}

	// Nobody else can set (or clear) servers on a thread that is still reserved: they would be
	// active for the creator's first message without the creator knowing.
	code, ec = a.call("PUT", e.base+"/threads/mine2/mcp-servers", mcpServersBody("", "Tools"), nil, nil)
	mustCode(t, "A reserves an ID", code, ec, 200, "")
	for what, body := range map[string]map[string]any{"sets": mcpServersBody("", "Theirs"), "clears": mcpServersBody("")} {
		code, ec = b.call("PUT", e.base+"/threads/mine2/mcp-servers", body, nil, nil)
		mustCode(t, "B "+what+" servers on A's reserved thread", code, ec, 404, "thread_not_found")
	}
	if names := e.scalar(`SELECT string_agg(name, ',') FROM thread_mcp_servers WHERE agent_id = $1 AND thread_id = 'mine2'`, e.agent); names != "Tools" {
		t.Fatalf("servers of A's reserved thread: %q", names)
	}
	if creator, _, _ := e.record(e.agent, "mine2"); creator != aID {
		t.Fatalf("creator %q", creator)
	}
	// Once the thread has its first message it is an ordinary shared thread: anyone who may use the agent may set its servers.
	code, ec = a.call("POST", e.base+"/threads/mine2/messages", map[string]any{"text": "Go"}, nil, nil)
	mustCode(t, "A's first message to mine2", code, ec, 202, "")
	code, ec = b.call("PUT", e.base+"/threads/mine2/mcp-servers", mcpServersBody("", "Theirs"), nil, nil)
	mustCode(t, "B sets servers on a started shared thread", code, ec, 200, "")
}
