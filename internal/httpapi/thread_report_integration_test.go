package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agents"
)

// The runtime reports a thread's status when a reply ends, and that report can arrive after the person deleted the
// thread (the delete asks the runtime, the report was already on its way). It must not bring the thread back: as a
// record nobody owns it would show the deleted thread's title to the whole team, also when it was a private thread.
func TestAStatusReportDoesNotResurrectADeletedThread(t *testing.T) {
	e := newThreadClaimEnv(t)
	ctx := context.Background()
	report := func(thread string, busy bool) error {
		now := time.Now().UnixMilli()
		return e.app.Agents.ReportThread(ctx, e.agentUUID(), agents.ThreadReport{
			ThreadID: thread, Title: "A deleted conversation", CreatedAt: now - 1000, Busy: busy, LastReplyAt: &now, At: now})
	}
	for _, visibility := range []string{"", "private"} {
		thread := "late-" + map[string]string{"": "shared", "private": "private"}[visibility]

		// The platform has the thread (a person started it), and a report updates what it has.
		body := map[string]any{"text": "Hi"}
		if visibility != "" {
			body["visibility"] = visibility
		}
		code, ec := e.editor.call("POST", e.base+"/threads/"+thread+"/messages", body, nil, nil)
		mustCode(t, "start the thread", code, ec, 202, "")
		waitReply(t, e.editor, e.base+"/threads/"+thread, 2)
		if err := report(thread, true); err != nil {
			t.Fatal(err)
		}
		if busy := e.scalar(`SELECT busy::text FROM agent_threads WHERE agent_id = $1 AND thread_id = $2`, e.agent, thread); busy != "true" {
			t.Fatalf("%s: a report did not update the thread it is about: busy = %s", thread, busy)
		}

		// The person deletes it, and a report that was already on its way arrives afterwards.
		code, ec = e.editor.call("DELETE", e.base+"/threads/"+thread, nil, nil, nil)
		mustCode(t, "delete the thread", code, ec, 200, "")
		if err := report(thread, false); err != nil {
			t.Fatalf("%s: a late report is an error instead of being ignored: %v", thread, err)
		}
		if n := e.threadRecords(e.agent, thread); n != "0" {
			t.Fatalf("%s: a late report brought the deleted thread back (%s records)", thread, n)
		}
		// The ID is free: someone else can start a private thread there, and gets no 409 from a ghost.
		code, ec = e.member.call("POST", e.base+"/threads/"+thread+"/messages", map[string]any{"text": "Mine now", "visibility": "private"}, nil, nil)
		mustCode(t, "start a private thread on the freed ID", code, ec, 202, "")
	}
}

// What the runtime lists when it starts is the truth about what it holds: threads the platform has no record of
// (created before the platform kept records, or lost with a restored backup) are adopted then, which a single report
// no longer does.
func TestTheRuntimesStartupSyncAdoptsThreadsThePlatformDoesNotKnow(t *testing.T) {
	e := newThreadClaimEnv(t)
	now := time.Now().UnixMilli()
	err := e.app.Agents.SyncThreads(context.Background(), e.agentUUID(), []agents.ThreadReport{
		{ThreadID: "legacy1", Title: "From before records", CreatedAt: now - 5000, LastReplyAt: &now, At: now},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.threadRecords(e.agent, "legacy1"); n != "1" {
		t.Fatalf("the sync did not adopt the runtime's thread (%s records)", n)
	}
}

// A thread the platform reserved (POST .../threads, or servers set on a new ID) has no conversation yet, so the runtime
// has never heard of it. Deleting it must still delete it, with its MCP servers, instead of a 404 that leaves the
// record (listed to its creator) for 30 days.
func TestAReservedThreadCanBeDeleted(t *testing.T) {
	e := newThreadClaimEnv(t)
	code, ec := e.editor.call("PUT", e.base+"/threads/reserved1/mcp-servers", mcpServersBody("private", "Tools"), nil, nil)
	mustCode(t, "reserve the thread with servers", code, ec, 200, "")
	if _, _, reserved := e.record(e.agent, "reserved1"); reserved != "true" {
		t.Fatalf("not reserved: %q", reserved)
	}
	// Someone else may not delete it (they can't see it).
	code, ec = e.member.call("DELETE", e.base+"/threads/reserved1", nil, nil, nil)
	mustCode(t, "another person deletes it", code, ec, 404, "thread_not_found")
	code, ec = e.editor.call("DELETE", e.base+"/threads/reserved1", nil, nil, nil)
	mustCode(t, "its creator deletes it", code, ec, 200, "")
	if n := e.threadRecords(e.agent, "reserved1"); n != "0" {
		t.Fatalf("the reserved thread is still recorded (%s)", n)
	}
	if n := e.serverCount(e.agent, "reserved1"); n != "0" {
		t.Fatalf("its servers remain (%s)", n)
	}
	// A thread nobody has (no record, nothing in the runtime) is still a 404.
	code, ec = e.editor.call("DELETE", e.base+"/threads/never-was", nil, nil, nil)
	mustCode(t, "delete a thread that never existed", code, ec, 404, "")
}
