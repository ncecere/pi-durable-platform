package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
)

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Tool approvals end to end (docs/approvals.md), on real runtimes and the demo model, which calls get_time when
// asked the time: the template version marks get_time, so the call waits until someone allowed decides.

type approvalEnv struct {
	*teamEnv
	agent, base string
}

func newApprovalEnv(t *testing.T) *approvalEnv {
	t.Helper()
	e := &approvalEnv{teamEnv: newTeamEnv(t)}
	var tpl apitypes.AgentTemplate
	code, ec := e.editor.call("POST", toolsTeam+"/agent-templates", map[string]any{"slug": "careful", "name": "Careful", "instructions": "Hi.",
		"approvals": map[string]any{"tools": []string{"get_*"}, "timeoutSeconds": 3600}}, &tpl, nil)
	mustCode(t, "template with approvals", code, ec, 201, "")
	var versions []apitypes.AgentTemplateVersion
	if code := e.editor.get("/v1/agent-templates/"+tpl.Id.String()+"/versions", &versions); code != 200 || len(versions) != 1 {
		t.Fatalf("versions: %d %+v", code, versions)
	}
	if p := versions[0].Approvals; p == nil || strings.Join(p.Tools, ",") != "get_*" || p.Role != "admin" || p.TimeoutSeconds != 3600 {
		t.Fatalf("version 1's approvals = %+v", p)
	}
	var ag apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Careful desk", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	e.agent, e.base = ag.Id.String(), "/v1/agents/"+ag.Id.String()
	return e
}

// pending waits until the thread has a pending approval (as s sees it) and returns it.
func (e *approvalEnv) pending(s *session, thread string) apitypes.ToolApproval {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var list []apitypes.ToolApproval
		if code := s.get(e.base+"/approvals?thread="+thread, &list); code == 200 && len(list) > 0 {
			return list[0]
		}
		sleep100()
	}
	e.t.Fatalf("no pending approval in %s", thread)
	return apitypes.ToolApproval{}
}

func (e *approvalEnv) auditActions() []string {
	e.t.Helper()
	rows, err := e.app.Pool.Query(context.Background(), `SELECT action FROM audit_log WHERE target_type = 'tool_approval' ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		out = append(out, a)
	}
	return out
}

// toolResult is the text of the thread's first tool result.
func toolResult(t *testing.T, v apitypes.ChatView) string {
	t.Helper()
	for _, m := range v.Messages {
		if m.Role == apitypes.ChatMessageRoleTool {
			return text(m)
		}
	}
	t.Fatalf("no tool result in %+v", v.Messages)
	return ""
}

func firstCall(t *testing.T, v apitypes.ChatView) apitypes.ChatToolCall {
	t.Helper()
	for _, m := range v.Messages {
		if m.ToolCalls != nil && len(*m.ToolCalls) > 0 {
			return (*m.ToolCalls)[0]
		}
	}
	t.Fatalf("no tool call in %+v", v.Messages)
	return apitypes.ChatToolCall{}
}

func TestAToolApprovalWaitsForSomeoneAllowedThenTheCallRuns(t *testing.T) {
	e := newApprovalEnv(t)
	thread := e.base + "/threads/shared-1"
	code, ec := e.member.call("POST", thread+"/messages", map[string]any{"text": "What time is it?"}, nil, nil)
	mustCode(t, "ask the time", code, ec, 202, "")

	ap := e.pending(e.member, "shared-1")
	if ap.Tool != "get_time" || ap.Status != apitypes.ToolApprovalStatusPending || !ap.CanDecide || ap.AgentName != "Careful desk" {
		t.Fatalf("the run's author sees %+v", ap)
	}
	if ap.RequestedFor == nil || ap.RequestedFor.Id == nil || *ap.RequestedFor.Id != e.member.me.User.Id.String() {
		t.Fatalf("requestedFor = %+v, want the member who asked", ap.RequestedFor)
	}
	if left := time.Until(ap.ExpiresAt); left < 50*time.Minute || left > 61*time.Minute {
		t.Fatalf("expires in %v, want the version's hour", left)
	}
	one := e.base + "/approvals/" + ap.Id

	// The thread's view shows the waiting call, and nothing ran.
	var view apitypes.ChatView
	if code := e.member.get(thread, &view); code != 200 || view.Approvals == nil || len(*view.Approvals) != 1 || (*view.Approvals)[0].Id != ap.Id {
		t.Fatalf("view approvals = %d %+v", code, view.Approvals)
	}
	if c := firstCall(t, view); c.Approval == nil || c.Approval.Status != apitypes.ToolApprovalStatusPending {
		t.Fatalf("the call's approval = %+v", c.Approval)
	}

	// Who may not decide: another team, platform staff (they can't use the agent), and an editor below the
	// version's role who didn't start the run (they see it, but may not decide).
	for name, s := range map[string]*session{"another team's person": e.outsider, "a platform admin": e.admin, "an auditor": e.auditor} {
		code, ec := s.call("GET", one, nil, nil, nil)
		mustCode(t, name+" reads it", code, ec, 404, "")
		code, ec = s.call("POST", one, map[string]any{"decision": "approve"}, nil, nil)
		mustCode(t, name+" decides", code, ec, 404, "")
	}
	var seen apitypes.ToolApproval
	if code := e.editor.get(one, &seen); code != 200 || seen.CanDecide {
		t.Fatalf("the editor sees it (%d) with canDecide %v", code, seen.CanDecide)
	}
	code, ec = e.editor.call("POST", one, map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "an editor who didn't start the run", code, ec, 403, "approval_forbidden")

	// Inboxes: the author and the owner have it, the editor doesn't.
	for name, want := range map[*session]bool{e.member: true, e.owner: true, e.editor: false, e.admin: false} {
		var inbox []apitypes.ToolApproval
		if code := name.get("/v1/me/approvals", &inbox); code != 200 || (len(inbox) == 1) != want {
			t.Fatalf("inbox of %s: %d %+v, want it there: %v", name.me.User.Email, code, inbox, want)
		}
	}

	var decided apitypes.ToolApproval
	code, ec = e.member.call("POST", one, map[string]any{"decision": "approve"}, &decided, nil)
	mustCode(t, "the author approves", code, ec, 200, "")
	if decided.Status != apitypes.ToolApprovalStatusApproved || decided.DecidedBy == nil || decided.DecidedBy.Name == "" || decided.CanDecide {
		t.Fatalf("decided = %+v", decided)
	}
	code, ec = e.owner.call("POST", one, map[string]any{"decision": "deny"}, nil, nil)
	mustCode(t, "decide again", code, ec, 409, "approval_decided")

	final := waitReply(t, e.member, thread, 4)
	if r := toolResult(t, final); !strings.Contains(r, "(UTC)") {
		t.Fatalf("the approved call's result = %q", r)
	}
	if c := firstCall(t, final); c.Approval == nil || c.Approval.Status != apitypes.ToolApprovalStatusApproved || c.Approval.DecidedBy == nil {
		t.Fatalf("the call's approval after the decision = %+v", c.Approval)
	}
	if final.Approvals != nil && len(*final.Approvals) > 0 {
		t.Fatalf("pending approvals left: %+v", *final.Approvals)
	}
	if got := strings.Join(e.auditActions(), ","); got != "tool_approval.request,tool_approval.decide" {
		t.Fatalf("audit = %s", got)
	}
	var entry struct {
		Meta json.RawMessage
	}
	if err := e.app.Pool.QueryRow(context.Background(), `SELECT metadata FROM audit_log WHERE action = 'tool_approval.decide'`).Scan(&entry.Meta); err != nil ||
		!strings.Contains(string(entry.Meta), `"decision": "approve"`) && !strings.Contains(string(entry.Meta), `"decision":"approve"`) {
		t.Fatalf("decide entry = %s (%v)", entry.Meta, err)
	}
	var decidedList []apitypes.ToolApproval
	if code := e.editor.get(e.base+"/approvals?status=decided", &decidedList); code != 200 || len(decidedList) != 1 {
		t.Fatalf("decided list = %d %+v", code, decidedList)
	}
}

func TestADeniedCallInAPrivateThreadIsTheCreatorsToDecide(t *testing.T) {
	e := newApprovalEnv(t)
	thread := e.base + "/threads/mine"
	code, ec := e.member.call("POST", thread+"/messages", map[string]any{"text": "What time is it?", "visibility": "private"}, nil, nil)
	mustCode(t, "ask in a private thread", code, ec, 202, "")
	ap := e.pending(e.member, "mine")
	one := e.base + "/approvals/" + ap.Id

	// The owner may decide shared threads' calls, but another's private thread isn't theirs to see.
	var list []apitypes.ToolApproval
	if code := e.owner.get(e.base+"/approvals", &list); code != 200 || len(list) != 0 {
		t.Fatalf("the owner lists %d %+v", code, list)
	}
	code, ec = e.owner.call("POST", one, map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "the owner decides another's private call", code, ec, 404, "approval_not_found")

	code, ec = e.member.call("POST", one, map[string]any{"decision": "deny", "reason": "Not now"}, nil, nil)
	mustCode(t, "the creator denies", code, ec, 200, "")
	final := waitReply(t, e.member, thread, 4)
	if r := toolResult(t, final); !strings.Contains(r, "Tool call blocked: denied by") || !strings.Contains(r, "Not now") {
		t.Fatalf("the denied call's result = %q", r)
	}
	if c := firstCall(t, final); c.Approval == nil || c.Approval.Status != apitypes.ToolApprovalStatusDenied || c.Approval.Reason == nil || *c.Approval.Reason != "Not now" {
		t.Fatalf("the call's approval = %+v", c.Approval)
	}
}

func TestADecisionWakesAHibernatedAgentAndTheCallRuns(t *testing.T) {
	e := newApprovalEnv(t)
	thread := e.base + "/threads/sleepy"
	code, ec := e.member.call("POST", thread+"/messages", map[string]any{"text": "What time is it?"}, nil, nil)
	mustCode(t, "ask the time", code, ec, 202, "")
	ap := e.pending(e.member, "sleepy")

	code, ec = e.member.call("POST", e.base+"/hibernate", nil, nil, nil)
	mustCode(t, "hibernate while it waits", code, ec, 200, "")
	var ag apitypes.Agent
	if e.member.get(e.base, &ag); ag.Running {
		t.Fatal("the agent still runs after hibernating")
	}
	// The owner decides (an unattended run's or not, owners and admins always may); the delivery wakes the agent,
	// whose hook asks again and finds the decision.
	code, ec = e.owner.call("POST", e.base+"/approvals/"+ap.Id, map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "the owner approves", code, ec, 200, "")
	final := waitReply(t, e.member, thread, 4)
	if r := toolResult(t, final); !strings.Contains(r, "(UTC)") {
		t.Fatalf("the call after waking = %q", r)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.scalar(`SELECT (delivered_at IS NOT NULL)::text FROM tool_approvals WHERE id = $1`, ap.Id) != "true" {
		if time.Now().After(deadline) {
			t.Fatal("the decision was never marked delivered")
		}
		sleep100()
	}
}

func TestAnUnansweredRequestTimesOutAndTheCallIsDenied(t *testing.T) {
	e := newApprovalEnv(t)
	thread := e.base + "/threads/late"
	code, ec := e.member.call("POST", thread+"/messages", map[string]any{"text": "What time is it?"}, nil, nil)
	mustCode(t, "ask the time", code, ec, 202, "")
	ap := e.pending(e.member, "late")
	if _, err := e.app.Pool.Exec(context.Background(), `UPDATE tool_approvals SET expires_at = now() - interval '1 second' WHERE id = $1`, ap.Id); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Agents.ApprovalsOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got apitypes.ToolApproval
	if code := e.member.get(e.base+"/approvals/"+ap.Id, &got); code != 200 || got.Status != apitypes.ToolApprovalStatusTimedOut || got.CanDecide {
		t.Fatalf("after the time-out: %d %+v", code, got)
	}
	code, ec = e.member.call("POST", e.base+"/approvals/"+ap.Id, map[string]any{"decision": "approve"}, nil, nil)
	mustCode(t, "decide after the time-out", code, ec, 409, "approval_decided")
	final := waitReply(t, e.member, thread, 4)
	if r := toolResult(t, final); !strings.Contains(r, "nobody approved it in time") {
		t.Fatalf("the timed-out call's result = %q", r)
	}
	if got := strings.Join(e.auditActions(), ","); got != "tool_approval.request,tool_approval.timeout" {
		t.Fatalf("audit = %s", got)
	}
}

func TestTemplateVersionsCarryTheirApprovals(t *testing.T) {
	e := newApprovalEnv(t)
	var tpls []apitypes.AgentTemplate
	if code := e.editor.get(toolsTeam+"/agent-templates", &tpls); code != 200 || len(tpls) != 1 {
		t.Fatalf("templates: %d", code)
	}
	path := "/v1/agent-templates/" + tpls[0].Id.String() + "/versions"
	// Omitted keeps the current version's, a field omitted inside too.
	code, ec := e.editor.call("POST", path, map[string]any{"instructions": "Hello.", "approvals": map[string]any{"role": "member"}}, nil, nil)
	mustCode(t, "version 2", code, ec, 201, "")
	code, ec = e.editor.call("POST", path, map[string]any{"instructions": "Hello again."}, nil, nil)
	mustCode(t, "version 3", code, ec, 201, "")
	for _, bad := range []map[string]any{{"tools": []string{"bad name"}}, {"role": "owner"}, {"timeoutSeconds": 5}} {
		code, ec = e.editor.call("POST", path, map[string]any{"instructions": "x", "approvals": bad}, nil, nil)
		mustCode(t, "bad approvals", code, ec, 400, "invalid_approvals")
	}
	var versions []apitypes.AgentTemplateVersion
	e.editor.get(path, &versions)
	if len(versions) != 3 {
		t.Fatalf("%d versions", len(versions))
	}
	for _, v := range versions[:2] {
		if p := v.Approvals; p == nil || strings.Join(p.Tools, ",") != "get_*" || p.Role != "member" || p.TimeoutSeconds != 3600 {
			t.Fatalf("version %d's approvals = %+v", v.Version, p)
		}
	}
}

// The runtime is not trusted: what it reports is bounded and redacted again, its expiry ignored, and a runtime
// that no longer has a request at its start cancels it.
func TestRuntimeReportsAreRedactedAndReconciled(t *testing.T) {
	e := newApprovalEnv(t)
	ctx := context.Background()
	id := e.app.Agents
	agentID := mustUUID(t, e.agent)
	now := time.Now().UnixMilli()
	report := agents.ApprovalReport{ID: "ap_reported1", ThreadID: "elsewhere", Tool: "mcp__crm__delete", CallID: "c9",
		Arguments: json.RawMessage(`{"record":"42","headers":{"Authorization":"Bearer s3cret"},"apiKey":"k"}`), RequestedAt: now,
		RequestedFor: &agents.ApprovalPrincipal{Kind: "user", ID: "not-a-uuid", Name: "Forged"}}
	ans, err := id.ReportApproval(ctx, agentID, report)
	if err != nil || ans.Status != "pending" || ans.ExpiresAt <= now {
		t.Fatalf("report: %+v %v", ans, err)
	}
	args := e.scalar(`SELECT arguments::text FROM tool_approvals WHERE id = 'ap_reported1'`)
	if strings.Contains(args, "s3cret") || !strings.Contains(args, `"apiKey": "[redacted]"`) || !strings.Contains(args, `"record": "42"`) {
		t.Fatalf("stored arguments = %s", args)
	}
	if kind := e.scalar(`SELECT coalesce(requested_for_kind, 'none') FROM tool_approvals WHERE id = 'ap_reported1'`); kind != "none" {
		t.Fatalf("a malformed requestedFor was kept: %s", kind)
	}
	// Reported again (a rerun of the runtime's hook): the same request, audited once.
	if _, err := id.ReportApproval(ctx, agentID, report); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(e.auditActions(), ","); got != "tool_approval.request" {
		t.Fatalf("audit after two reports = %s", got)
	}
	// The runtime starts without it: cancelled.
	if _, err := id.SyncApprovals(ctx, agentID, nil, now+1000); err != nil {
		t.Fatal(err)
	}
	if st := e.scalar(`SELECT status FROM tool_approvals WHERE id = 'ap_reported1'`); st != "cancelled" {
		t.Fatalf("status after the sync = %s", st)
	}
	if got := strings.Join(e.auditActions(), ","); got != "tool_approval.request,tool_approval.cancel" {
		t.Fatalf("audit = %s", got)
	}
}
