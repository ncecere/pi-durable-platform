package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/testutil"
)

// scheduleCall is the runtime's schedule tool calling the gateway with the
// agent's credential (cred "" sends none).
func scheduleCall(t *testing.T, gatewayURL, method, path, cred string, body any) (int, map[string]any, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = strings.NewReader(string(b))
	}
	req, _ := http.NewRequest(method, gatewayURL+path, rdr)
	if cred != "" {
		req.Header.Set("Authorization", "Bearer "+cred)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var env struct {
		Data  any
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(raw, &env)
	data, _ := env.Data.(map[string]any)
	if list, ok := env.Data.([]any); ok {
		data = map[string]any{"list": list}
	}
	return res.StatusCode, data, env.Error.Code
}

// An agent sets its own reminders through the gateway, with its own
// credential: only when its template version allows it, for itself only.
// People see the reminder in the Triggers API, marked as the agent's, and
// the audit log names the agent. The reminder wakes the agent in the thread
// it was set in, a personal workspace owner's private thread included, and
// removes itself.
func TestAgentMadeSchedulesThroughTheGateway(t *testing.T) {
	app := newTestAppWith(t, nil, testutil.Logger(), nil, true)
	e := newTeamEnvOn(t, app)
	var tpl, plain apitypes.AgentTemplate
	code, ec := e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "reminders", "name": "Reminders", "instructions": "Hi.",
		"agentSchedules": true}, &tpl, nil)
	mustCode(t, "a template whose agents schedule", code, ec, 201, "")
	code, ec = e.admin.call("POST", "/v1/agent-templates", map[string]any{"slug": "plain", "name": "Plain", "instructions": "Hi."}, &plain, nil)
	mustCode(t, "a template without", code, ec, 201, "")
	var versions []apitypes.AgentTemplateVersion
	if code := e.admin.get("/v1/agent-templates/"+tpl.Id.String()+"/versions", &versions); code != 200 || len(versions) != 1 ||
		versions[0].AgentSchedules == nil || !*versions[0].AgentSchedules {
		t.Fatalf("versions = %d %+v", code, versions)
	}

	var ag, other, mine apitypes.Agent
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Planner", "templateId": tpl.Id}, &ag, nil)
	mustCode(t, "deploy", code, ec, 201, "")
	code, ec = e.editor.call("POST", toolsTeam+"/agents", map[string]any{"name": "Plain bot", "templateId": plain.Id}, &other, nil)
	mustCode(t, "deploy another", code, ec, 201, "")
	code, ec = e.owner.call("POST", "/v1/teams/"+personalSlug(t, e.owner)+"/agents", map[string]any{"name": "My planner", "templateId": tpl.Id}, &mine, nil)
	mustCode(t, "deploy in a personal workspace", code, ec, 201, "")
	if got := e.spec(ag.Id).Env["PLATFORM_SCHEDULES"]; got != "true" {
		t.Fatalf("the runtime isn't told it may schedule: %q", got)
	}
	if _, ok := e.spec(other.Id).Env["PLATFORM_SCHEDULES"]; ok {
		t.Fatal("a runtime whose version doesn't allow it is told it may schedule")
	}
	// The runtime's own credential (PLATFORM_KEY), as it reports thread status.
	platformKey := func(email string, id uuid.UUID) string {
		spec, err := app.Agents.Spec(context.Background(), authz.Actor{UserID: e.userID(email), PlatformRole: authz.PlatformNone}, id)
		if err != nil || spec.Env["PLATFORM_KEY"] == "" {
			t.Fatalf("spec: %v", err)
		}
		return spec.Env["PLATFORM_KEY"]
	}
	cred, otherCred := platformKey("blair@localhost", ag.Id), platformKey("blair@localhost", other.Id)
	mineCred := platformKey("user@localhost", mine.Id)

	// A shared thread to set it in.
	base := "/v1/agents/" + ag.Id.String()
	code, ec = e.member.call("POST", base+"/threads/s1/messages", map[string]any{"text": "Remind me"}, nil, nil)
	mustCode(t, "a shared thread", code, ec, 202, "")
	waitReply(t, e.member, base+"/threads/s1", 2)

	at := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339)
	body := map[string]any{"at": at, "message": "Check the build", "threadId": "s1"}
	if code, _, ec := scheduleCall(t, app.GatewayURL, "POST", "/schedules", "", body); code != 401 || ec != "unauthorized" {
		t.Fatalf("without a credential: %d %s", code, ec)
	}
	if code, _, ec := scheduleCall(t, app.GatewayURL, "POST", "/schedules", otherCred, body); code != 403 || ec != "schedules_not_allowed" {
		t.Fatalf("a version that doesn't allow it: %d %s", code, ec)
	}
	code, made, ec := scheduleCall(t, app.GatewayURL, "POST", "/schedules", cred, body)
	if code != 201 || made["where"] != "this" {
		t.Fatalf("schedule: %d %v %s", code, made, ec)
	}
	sched, _ := made["schedule"].(map[string]any)
	id, _ := sched["id"].(string)
	if sched["once"] != true || sched["threadId"] != "s1" || sched["message"] != "Check the build" {
		t.Fatalf("schedule = %v", sched)
	}
	// Recurring ones: at least every TRIGGERS_AGENT_MIN_INTERVAL.
	if code, _, ec := scheduleCall(t, app.GatewayURL, "POST", "/schedules", cred, map[string]any{"cron": "* * * * *", "message": "x"}); code != 400 || ec != "too_frequent" {
		t.Fatalf("every minute: %d %s", code, ec)
	}
	// Its own only.
	if code, list, _ := scheduleCall(t, app.GatewayURL, "GET", "/schedules", cred, nil); code != 200 || len(list["list"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	if code, list, _ := scheduleCall(t, app.GatewayURL, "GET", "/schedules", otherCred, nil); code != 200 || len(list["list"].([]any)) != 0 {
		t.Fatalf("another agent's list = %d %v", code, list)
	}
	if code, _, ec := scheduleCall(t, app.GatewayURL, "DELETE", "/schedules/"+id, otherCred, nil); code != 404 || ec != "trigger_not_found" {
		t.Fatalf("another agent cancelled it: %d %s", code, ec)
	}

	// People see it, marked as the agent's, in the thread it was set in.
	var listed []apitypes.Trigger
	if code := e.member.get(base+"/triggers", &listed); code != 200 || len(listed) != 1 || listed[0].Source != "agent" || !listed[0].Once ||
		listed[0].OriginThreadId == nil || *listed[0].OriginThreadId != "s1" || listed[0].CreatedBy != nil {
		t.Fatalf("listed = %d %+v", code, listed)
	}
	var page struct {
		Data struct{ Items []apitypes.AuditEntry } `json:"data"`
	}
	code, raw := e.owner.raw("GET", toolsTeam+"/audit?action=trigger.create", nil, nil)
	_ = json.Unmarshal(raw, &page)
	entries := page.Data.Items
	if code != 200 || len(entries) != 1 || entries[0].Actor.Kind != "agent" || entries[0].Actor.AgentId == nil ||
		*entries[0].Actor.AgentId != ag.Id || entries[0].Actor.DisplayName == nil || *entries[0].Actor.DisplayName != "Planner" {
		t.Fatalf("audit = %d %s", code, raw)
	}

	// It fires: the agent is woken in s1, with the reminder as its message.
	tick := func() {
		app.Triggers.Wait()
		if err := app.Triggers.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * time.Second)
	waitFor(t, "the reminder's run", func() bool {
		tick()
		var runs []apitypes.TriggerRun
		e.member.get(base+"/runs", &runs)
		return len(runs) == 1 && runs[0].Status == "succeeded"
	})
	view := waitReply(t, e.member, base+"/threads/s1", 4)
	reminder := view.Messages[2]
	if reminder.Author == nil || reminder.Author.Kind != "trigger" || text(reminder) != "Check the build" {
		t.Fatalf("the reminder's message = %+v", reminder)
	}
	waitFor(t, "the reminder to remove itself", func() bool {
		tick()
		return e.scalar(`SELECT count(*)::text FROM agent_triggers WHERE id = $1`, id) == "0"
	})

	// A personal workspace: the owner's private thread takes the agent's own reminder.
	ws := "/v1/agents/" + mine.Id.String()
	code, ec = e.owner.call("POST", ws+"/threads/p1/messages", map[string]any{"text": "Remind me later", "visibility": "private"}, nil, nil)
	mustCode(t, "a private thread", code, ec, 202, "")
	waitReply(t, e.owner, ws+"/threads/p1", 2)
	at = time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	code, made, ec = scheduleCall(t, app.GatewayURL, "POST", "/schedules", mineCred, map[string]any{"at": at, "message": "Your reminder", "threadId": "p1"})
	if code != 201 || made["where"] != "this" {
		t.Fatalf("in the owner's private thread: %d %v %s", code, made, ec)
	}
	time.Sleep(2 * time.Second)
	waitFor(t, "the private thread's reminder", func() bool {
		tick()
		var v apitypes.ChatView
		e.owner.get(ws+"/threads/p1", &v)
		return len(v.Messages) >= 3 && text(v.Messages[2]) == "Your reminder"
	})

	// The owner deletes an agent's schedule; the agent's tool no longer lists it.
	code, made, _ = scheduleCall(t, app.GatewayURL, "POST", "/schedules", cred, map[string]any{"cron": "0 9 * * 1", "message": "Weekly", "thread": "new"})
	if code != 201 || made["where"] != "new" {
		t.Fatalf("weekly: %d %v", code, made)
	}
	weekly := uuid.MustParse(made["schedule"].(map[string]any)["id"].(string))
	code, ec = e.editor.call("DELETE", base+"/triggers/"+weekly.String(), nil, nil, nil)
	mustCode(t, "an editor deletes it", code, ec, 403, "forbidden")
	code, ec = e.owner.call("DELETE", base+"/triggers/"+weekly.String(), nil, nil, nil)
	mustCode(t, "the owner deletes it", code, ec, 200, "")
	if code, list, _ := scheduleCall(t, app.GatewayURL, "GET", "/schedules", cred, nil); code != 200 || len(list["list"].([]any)) != 0 {
		t.Fatalf("after the owner's delete: %d %v", code, list)
	}
}
