package triggers_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// Agent-made schedules (docs/triggers.md): the agent's schedule tool, through
// the gateway, with the agent's own credential. The engine side: who may,
// the limits, where a reminder fires, that a one-time reminder fires once and
// removes itself, that an agent touches only its own, and that people manage
// them like any trigger.

// allowSchedules lets (or not) the assistant template's version schedule.
func (e *env) allowSchedules(on bool) {
	e.exec(`UPDATE agent_template_versions SET agent_schedules = $1
		WHERE template_id = (SELECT id FROM agent_templates WHERE slug = 'assistant')`, on)
}

// thread records one of the agent's threads as the platform would.
func (e *env) thread(agentID uuid.UUID, id, visibility string, creator *uuid.UUID) {
	e.exec(`INSERT INTO agent_threads (agent_id, thread_id, title, busy, status_at, created_at, updated_at, visibility, created_by_user)
		VALUES ($1, $2, 'A chat', false, $3, $3, $3, $4, $5)`, agentID, id, e.clock.Now(), visibility, creator)
}

func (e *env) agentSchedule(agentID uuid.UUID, in triggers.AgentScheduleInput) (triggers.AgentScheduled, error) {
	return e.svc.AgentCreate(context.Background(), agentID, in)
}

func TestAnAgentSchedulesListsAndCancelsItsOwn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.thread(e.agentID, "chat1", "shared", nil)
	in := triggers.AgentScheduleInput{At: "2026-10-20T12:30", Timezone: "Europe/Paris", Message: "Remind them about the report", ThreadID: "chat1"}

	// A version that doesn't allow it: refused, with what to do.
	if _, err := e.agentSchedule(e.agentID, in); !isCode(err, "schedules_not_allowed") {
		t.Fatalf("without the version's flag: %v", err)
	}
	e.allowSchedules(true)
	made, err := e.agentSchedule(e.agentID, in)
	if err != nil {
		t.Fatal(err)
	}
	tr := made.Trigger
	// 12:30 in Paris is 10:30 UTC; its cron names the same minute, in its zone.
	if made.Where != triggers.WhereThis || tr.Source != triggers.SourceAgent || !tr.Once || tr.ThreadID == nil || *tr.ThreadID != "chat1" ||
		tr.OriginThreadID == nil || *tr.OriginThreadID != "chat1" || tr.CreatedBy.Valid || *tr.Cron != "30 12 20 10 *" ||
		!tr.NextFireAt.Equal(time.Date(2026, 10, 20, 10, 30, 0, 0, time.UTC)) || tr.Name != "Reminder 2026-10-20 12:30 Europe/Paris" {
		t.Fatalf("made = %+v %+v", made, tr)
	}
	weekly, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "0 9 * * 1", Message: "Weekly summary", ThreadID: "chat1", Thread: "new"})
	if err != nil || weekly.Where != triggers.WhereNew || weekly.Trigger.ThreadID != nil || weekly.Trigger.Once ||
		*weekly.Trigger.OriginThreadID != "chat1" || *weekly.Trigger.MissedPolicy != triggers.MissedSkip {
		t.Fatalf("weekly = %+v (%v)", weekly, err)
	}
	// A person's trigger is listed to people, never to the agent's tool.
	e.schedule("Nightly", "0 2 * * *", triggers.MissedSkip)
	list, err := e.svc.AgentList(ctx, e.agentID)
	// Exactly the two the agent made (in creation order, but two made within the same microsecond tie on created_at and
	// then go by id, so the test doesn't depend on the order).
	listed := map[uuid.UUID]bool{}
	for _, l := range list {
		listed[l.Trigger.ID] = true
	}
	if err != nil || len(list) != 2 || !listed[tr.ID] || !listed[weekly.Trigger.ID] {
		t.Fatalf("the agent's list = %+v (%v)", list, err)
	}
	// Created and cancelled by the agent: the audit log names it as the actor.
	if _, err := e.svc.AgentCancel(ctx, e.agentID, weekly.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.AgentList(ctx, e.agentID); len(list) != 1 {
		t.Fatalf("after cancelling, %d", len(list))
	}
	for action, want := range map[string]string{"trigger.create": "2", "trigger.delete": "1"} {
		got := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = $1 AND actor_kind = 'agent' AND actor_user_id IS NULL
			AND metadata->>'agentActorId' = $2 AND metadata->>'agentActorName' = 'helper'`, action, e.agentID.String())
		if got != want {
			t.Errorf("%s by the agent: %s entries, want %s", action, got, want)
		}
	}
	// The message is the conversation's: never in an entry (nor in a name).
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE concat(after_state::text, before_state::text, metadata::text) LIKE ANY (ARRAY['%about the report%', '%Weekly summary%'])`); n != "0" {
		t.Errorf("a message in the audit log (%s)", n)
	}
}

func TestAnAgentTouchesOnlyItsOwnSchedules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.allowSchedules(true)
	other := e.newAgent("other")
	mine, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.AgentCancel(ctx, other, mine.Trigger.ID); !isCode(err, "trigger_not_found") {
		t.Fatalf("another agent cancelled it: %v", err)
	}
	if list, _ := e.svc.AgentList(ctx, other); len(list) != 0 {
		t.Fatalf("another agent lists %d", len(list))
	}
	// Nor a person's trigger of its own agent.
	person := e.schedule("Nightly", "0 2 * * *", triggers.MissedSkip)
	if _, err := e.svc.AgentCancel(ctx, e.agentID, person.Trigger.ID); !isCode(err, "trigger_not_found") {
		t.Fatalf("the agent cancelled a person's trigger: %v", err)
	}
	// Another agent's thread isn't "this" one: an unknown thread to this agent.
	e.thread(other, "theirs", "shared", nil)
	made, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "Here?", ThreadID: "theirs"})
	if err != nil || made.Where != triggers.WhereNew || made.Trigger.OriginThreadID != nil || !strings.Contains(made.WhyNotHere, "no record") {
		t.Fatalf("another agent's thread: %+v (%v)", made, err)
	}
}

func TestAgentMadeLimitsRefuseWithAClearMessage(t *testing.T) {
	e := newEnv(t)
	e.allowSchedules(true)
	for _, c := range []struct {
		in   triggers.AgentScheduleInput
		code string
	}{
		{triggers.AgentScheduleInput{Cron: "* * * * *", Message: "x"}, "too_frequent"},
		{triggers.AgentScheduleInput{Cron: "0,2 9 * * *", Message: "x"}, "too_frequent"},
		{triggers.AgentScheduleInput{Cron: "@every 1h", Message: "x"}, "invalid_cron"},
		{triggers.AgentScheduleInput{At: "2026-10-20T09:00:00Z", Message: "x"}, "time_passed"},
		{triggers.AgentScheduleInput{At: "2027-12-01T09:00", Message: "x"}, "too_far_ahead"},
		{triggers.AgentScheduleInput{At: "tomorrow", Message: "x"}, "invalid_at"},
		{triggers.AgentScheduleInput{At: "2026-10-21T09:00", Cron: "@daily", Message: "x"}, "invalid_when"},
		{triggers.AgentScheduleInput{Message: "x"}, "invalid_when"},
		{triggers.AgentScheduleInput{Cron: "@daily", Message: "  "}, "invalid_message"},
		{triggers.AgentScheduleInput{Cron: "@daily", Message: "x", Timezone: "Mars/Olympus"}, "invalid_timezone"},
		{triggers.AgentScheduleInput{Cron: "@daily", Message: "x", Thread: "elsewhere"}, "invalid_thread"},
	} {
		if _, err := e.agentSchedule(e.agentID, c.in); !isCode(err, c.code) {
			t.Errorf("%+v: %v, want %s", c.in, err, c.code)
		}
	}
	// The interval is the minimum: every 5 minutes is allowed.
	for i := 0; i < 3; i++ {
		if _, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "*/5 * * * *", Message: "Check the queue"}); err != nil {
			t.Fatalf("schedule %d: %v", i, err)
		}
	}
	// At most AgentMax (3 here) of the agent's own; the message says what to do.
	_, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "One more"})
	if !isCode(err, "too_many_schedules") || !strings.Contains(err.Error(), "cancel one") {
		t.Fatalf("over the limit: %v", err)
	}
	// Unique names: the same message three times.
	list, _ := e.svc.AgentList(context.Background(), e.agentID)
	names := map[string]bool{}
	for _, v := range list {
		names[v.Trigger.Name] = true
	}
	for _, want := range []string{"Schedule */5 * * * * UTC", "Schedule */5 * * * * UTC (2)", "Schedule */5 * * * * UTC (3)"} {
		if !names[want] {
			t.Errorf("no %q in %v", want, names)
		}
	}
	// A person's triggers don't count against the agent's own.
	e.schedule("Nightly", "0 2 * * *", triggers.MissedSkip)
	// An app's agent for one of its users, and an archived team's agent, can't.
	e.exec(`UPDATE agents SET service_managed = true, external_key = 'app-user-1' WHERE id = $1`, e.agentID)
	if _, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "x"}); !isCode(err, "schedules_not_allowed") {
		t.Errorf("a service-managed agent: %v", err)
	}
	e.exec(`UPDATE agents SET service_managed = false, external_key = NULL WHERE id = $1`, e.agentID)
	e.exec(`UPDATE teams SET status = 'archived' WHERE id = $1`, e.teamID)
	if _, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "x"}); !isCode(err, "team_archived") {
		t.Errorf("an archived team's agent: %v", err)
	}
}

// A one-time reminder fires once, at its time (the test's clock), into the
// thread it was set in, with the agent's text as it wrote it; then it is
// gone, and its run says it was the agent's.
func TestAOneTimeReminderFiresOnceIntoItsThreadAndRemovesItself(t *testing.T) {
	e := newEnv(t)
	e.allowSchedules(true)
	e.thread(e.agentID, "chat1", "shared", nil)
	text := "Check whether {{fire.time}} the deploy finished"
	made, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{At: "2026-10-20T10:31:30Z", Message: text, ThreadID: "chat1"})
	if err != nil || made.Where != triggers.WhereThis {
		t.Fatalf("%+v %v", made, err)
	}
	e.clock.Set(time.Date(2026, 10, 20, 10, 31, 0, 0, time.UTC))
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 0 {
		t.Fatalf("fired early: %d", n)
	}
	e.clock.Set(time.Date(2026, 10, 20, 10, 31, 35, 0, time.UTC))
	e.tick(e.svc)
	sent := e.runner.sends()
	if len(sent) != 1 || sent[0].ThreadID != "chat1" || sent[0].Text != text || !sent[0].AgentMade || sent[0].TriggerID != made.Trigger.ID {
		t.Fatalf("sent = %+v", sent)
	}
	// It has no next time; it removes itself when its run ends.
	if next := e.scalar(`SELECT coalesce(next_fire_at::text, 'none') FROM agent_triggers WHERE id = $1`, made.Trigger.ID); next != "none" {
		t.Fatalf("a fired reminder's next time: %s", next)
	}
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 1 {
		t.Fatalf("fired %d times", n)
	}
	e.replyEnds("chat1", false)
	e.tick(e.svc)
	if n := e.scalar(`SELECT count(*)::text FROM agent_triggers WHERE id = $1`, made.Trigger.ID); n != "0" {
		t.Fatal("the reminder is still there after its run")
	}
	runs := e.runs()
	wantStatus(t, runs[0], triggers.StatusSucceeded, "")
	if len(runs) != 1 || runs[0].Run.TriggerSource != triggers.SourceAgent || *runs[0].Run.ThreadID != "chat1" {
		t.Fatalf("runs = %+v", runs)
	}
	if got := e.audits("trigger.delete"); len(got) != 1 || !strings.Contains(got[0], `"reason": "fired"`) {
		t.Fatalf("the removal's audit: %v", got)
	}
	// Never again.
	e.clock.Add(366 * 24 * time.Hour)
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 1 {
		t.Fatalf("fired %d times", n)
	}
}

// Where a schedule fires: its thread when that thread may take a trigger's
// message (the triggers rules), otherwise a new thread, and the answer says
// why. A shared team's private thread takes none at all (its text would
// reach the team); in a personal workspace the owner's private threads may.
func TestWhereAnAgentMadeScheduleFires(t *testing.T) {
	e := newEnv(t)
	e.allowSchedules(true)
	e.thread(e.agentID, "private1", "private", &e.owner.UserID)
	e.thread(e.agentID, "apps1", "shared", nil)
	e.exec(`INSERT INTO thread_mcp_servers (agent_id, thread_id, name, slug, url, expires_at) VALUES ($1, 'apps1', 'Mail', 'mail', 'https://mcp.example.com', now() + interval '1 hour')`, e.agentID)
	// A shared team's private thread: refused, whichever thread it would fire in, with what to do.
	for _, where := range []string{"", triggers.WhereThis, triggers.WhereNew} {
		_, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "My salary is low", ThreadID: "private1", Thread: where})
		if !isCode(err, "schedule_private_thread") || !strings.Contains(err.Error(), "shared conversation") {
			t.Errorf("from a private thread (thread %q): %v", where, err)
		}
	}
	if n := e.scalar(`SELECT count(*)::text FROM agent_triggers WHERE agent_id = $1`, e.agentID); n != "0" {
		t.Fatalf("%s triggers made from a private thread", n)
	}
	for _, c := range []struct{ thread, why string }{
		{"apps1", "MCP servers"}, {"nowhere", "no record"},
	} {
		made, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "In " + c.thread, ThreadID: c.thread})
		if err != nil || made.Where != triggers.WhereNew || made.Trigger.ThreadID != nil || !strings.Contains(made.WhyNotHere, c.why) {
			t.Errorf("%s: %+v (%v)", c.thread, made, err)
		}
		if _, err := e.svc.AgentCancel(context.Background(), e.agentID, made.Trigger.ID); err != nil {
			t.Fatal(err)
		}
	}

	// A personal workspace: its owner's private thread may.
	e.exec(`UPDATE teams SET kind = 'personal', personal_owner = $2 WHERE id = $1`, e.teamID, e.owner.UserID)
	made, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{At: "2026-10-20T11:00:00Z", Message: "Your reminder", ThreadID: "private1"})
	if err != nil || made.Where != triggers.WhereThis || *made.Trigger.ThreadID != "private1" {
		t.Fatalf("the owner's private thread: %+v (%v)", made, err)
	}
	// Someone else's private thread in it (a person no longer the owner) may not.
	e.thread(e.agentID, "private2", "private", &e.member.UserID)
	if other, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "x", ThreadID: "private2"}); !isCode(err, "schedule_private_thread") {
		t.Fatalf("another person's private thread: %+v (%v)", other, err)
	}

	// If the thread can no longer take it when it fires (an app gave it
	// servers meanwhile), it fires into a new thread.
	e.exec(`INSERT INTO thread_mcp_servers (agent_id, thread_id, name, slug, url, expires_at) VALUES ($1, 'private1', 'Mail', 'mail', 'https://mcp.example.com', now() + interval '1 hour')`, e.agentID)
	e.clock.Set(time.Date(2026, 10, 20, 11, 0, 5, 0, time.UTC))
	e.tick(e.svc)
	sent := e.runner.sends()
	if len(sent) != 1 || sent[0].ThreadID == "private1" || !strings.HasPrefix(sent[0].ThreadID, "t_") {
		t.Fatalf("sent = %+v", sent)
	}
}

// People see agent-made schedules with the agent's mark, and owners and
// admins pause, resume and delete them; nothing else about them changes.
// Every trigger limit holds: a paused agent's reminder is recorded as
// skipped, and removed.
func TestPeopleManageAgentMadeSchedules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.allowSchedules(true)
	made, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{At: "2026-10-20T11:00:00Z", Message: "Ping"})
	if err != nil {
		t.Fatal(err)
	}
	id := made.Trigger.ID
	got, err := e.svc.Get(ctx, e.member, e.agentID, id)
	if err != nil || got.Trigger.Source != triggers.SourceAgent || !got.Trigger.Once {
		t.Fatalf("a member reads it: %+v (%v)", got, err)
	}
	if _, err := e.svc.Update(ctx, e.owner, e.agentID, id, triggers.Input{Name: ptr("Mine now")}, got.Trigger.Revision); !isCode(err, "agent_made_trigger") {
		t.Fatalf("renamed: %v", err)
	}
	if err := e.svc.Delete(ctx, e.member, e.agentID, id); !isCode(err, "forbidden") {
		t.Fatalf("a member deleted it: %v", err)
	}
	paused, err := e.svc.Update(ctx, e.admin, e.agentID, id, triggers.Input{Paused: ptr(true)}, got.Trigger.Revision)
	if err != nil {
		t.Fatal(err)
	}
	// Paused past its time: nothing fires. Resumed, it fires at the next pass.
	e.clock.Set(time.Date(2026, 10, 20, 11, 5, 0, 0, time.UTC))
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 0 {
		t.Fatalf("a paused reminder fired")
	}
	resumed, err := e.svc.Update(ctx, e.owner, e.agentID, id, triggers.Input{Paused: ptr(false)}, paused.Trigger.Revision)
	if err != nil || !resumed.Trigger.NextFireAt.Equal(e.clock.Now()) {
		t.Fatalf("resumed: %+v (%v)", resumed.Trigger, err)
	}
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 1 {
		t.Fatalf("the resumed reminder fired %d times", n)
	}

	// The agent's limits: all its triggers paused, the reminder's time comes.
	again, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{At: "2026-10-20T12:00:00Z", Message: "Ping again"})
	if err != nil {
		t.Fatal(err)
	}
	e.setLimits(triggers.Settings{Paused: true})
	e.clock.Set(time.Date(2026, 10, 20, 12, 0, 5, 0, time.UTC))
	e.tick(e.svc)
	if n := len(e.runner.sends()); n != 1 {
		t.Fatalf("a paused agent's reminder was sent")
	}
	runs := e.runs()
	wantStatus(t, runs[0], triggers.StatusSkipped, triggers.ReasonAgentPaused)
	if n := e.scalar(`SELECT count(*)::text FROM agent_triggers WHERE id = $1`, again.Trigger.ID); n != "0" {
		t.Fatal("a skipped reminder is still there")
	}

	// The owner deletes one: audited as the owner.
	recurring, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "Daily"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.owner, e.agentID, recurring.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'trigger.delete' AND actor_user_id = $1 AND target_id = $2`,
		e.owner.UserID, recurring.Trigger.ID.String()); n != "1" {
		t.Fatalf("the owner's delete: %s entries", n)
	}
}

// An agent-made trigger's message is conversation-derived text: platform
// staff never read it (a shared team's agent or a personal workspace's, from
// a shared thread or none), and people who may use the agent read it only
// when they may read the thread it was made in. People's triggers' messages
// are the team's settings, shown as before.
func TestAgentMadeMessagesAreWithheld(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.allowSchedules(true)
	e.thread(e.agentID, "shared1", "shared", nil)
	fromShared, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "From the shared chat", ThreadID: "shared1"})
	if err != nil {
		t.Fatal(err)
	}
	fromNowhere, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@weekly", Message: "From a subagent"})
	if err != nil {
		t.Fatal(err)
	}
	// One made before private threads were refused: from the member's private thread.
	e.thread(e.agentID, "private1", "private", &e.member.UserID)
	fromPrivate, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@monthly", Message: "x", ThreadID: "shared1"})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE agent_triggers SET origin_thread_id = 'private1', message_template = 'My private worry' WHERE id = $1`, fromPrivate.Trigger.ID)
	person := e.schedule("Nightly", "0 2 * * *", triggers.MissedSkip)

	shown := func(a authz.Actor) map[uuid.UUID]string {
		t.Helper()
		list, err := e.svc.List(ctx, a, e.agentID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]string{}
		for _, v := range list {
			if v.MessageHidden != (v.Trigger.MessageTemplate == "") {
				t.Fatalf("hidden %v with message %q", v.MessageHidden, v.Trigger.MessageTemplate)
			}
			out[v.Trigger.ID] = v.Trigger.MessageTemplate
			one, err := e.svc.Get(ctx, a, e.agentID, v.Trigger.ID)
			if err != nil || one.MessageHidden != v.MessageHidden || one.Trigger.MessageTemplate != v.Trigger.MessageTemplate {
				t.Fatalf("Get differs from List: %+v %v", one, err)
			}
		}
		return out
	}
	check := func(who string, a authz.Actor, want map[uuid.UUID]string) {
		t.Helper()
		got := shown(a)
		for id, msg := range want {
			if got[id] != msg {
				t.Errorf("%s reads %q, want %q", who, got[id], msg)
			}
		}
	}
	personMsg := person.Trigger.MessageTemplate
	// The member whose private thread it was reads all of them.
	check("the private thread's member", e.member, map[uuid.UUID]string{fromShared.Trigger.ID: "From the shared chat",
		fromNowhere.Trigger.ID: "From a subagent", fromPrivate.Trigger.ID: "My private worry", person.Trigger.ID: personMsg})
	// The team's owner and its service account: not another's private thread's.
	for who, a := range map[string]authz.Actor{"the owner": e.owner, "the service account": e.service} {
		check(who, a, map[uuid.UUID]string{fromShared.Trigger.ID: "From the shared chat", fromNowhere.Trigger.ID: "From a subagent",
			fromPrivate.Trigger.ID: "", person.Trigger.ID: personMsg})
	}
	// Platform staff: no agent-made message at all, a person's as before.
	admin := authz.Actor{UserID: uuid.New(), PlatformRole: authz.PlatformAdmin}
	e.exec(`INSERT INTO users (id, oidc_issuer, oidc_subject, email, platform_role) VALUES ($1, 'test', $2, $3, 'platform_admin')`,
		admin.UserID, admin.UserID.String(), admin.UserID.String()+"@example.com")
	staffWant := map[uuid.UUID]string{fromShared.Trigger.ID: "", fromNowhere.Trigger.ID: "", fromPrivate.Trigger.ID: "", person.Trigger.ID: personMsg}
	check("an auditor", e.staff, staffWant)
	check("a platform admin", admin, staffWant)
	// Pausing returns the trigger as the caller may see it.
	cur, err := e.svc.Get(ctx, e.owner, e.agentID, fromPrivate.Trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := e.svc.Update(ctx, e.owner, e.agentID, fromPrivate.Trigger.ID, triggers.Input{Paused: ptr(true)}, cur.Trigger.Revision)
	if err != nil || !paused.MessageHidden || paused.Trigger.MessageTemplate != "" {
		t.Fatalf("the owner pauses it: %+v (%v)", paused.View, err)
	}
	// A deleted origin thread: withheld (nobody can tell whose it was).
	e.exec(`DELETE FROM agent_threads WHERE agent_id = $1 AND thread_id = 'shared1'`, e.agentID)
	check("the owner, once the thread is gone", e.owner, map[uuid.UUID]string{fromShared.Trigger.ID: ""})

	// A personal workspace: the owner reads their agent's messages, platform staff don't.
	e.exec(`UPDATE teams SET kind = 'personal', personal_owner = $2 WHERE id = $1`, e.teamID, e.owner.UserID)
	e.exec(`DELETE FROM team_members WHERE team_id = $1 AND user_id <> $2`, e.teamID, e.owner.UserID)
	e.thread(e.agentID, "mine", "private", &e.owner.UserID)
	e.exec(`DELETE FROM agent_triggers WHERE id = $1`, fromPrivate.Trigger.ID) // room for one more (AgentMax 3)
	mine, err := e.agentSchedule(e.agentID, triggers.AgentScheduleInput{Cron: "@daily", Message: "Call the dentist", ThreadID: "mine", Thread: triggers.WhereNew})
	if err != nil {
		t.Fatal(err)
	}
	check("the workspace's owner", e.owner, map[uuid.UUID]string{mine.Trigger.ID: "Call the dentist", fromNowhere.Trigger.ID: "From a subagent"})
	check("an auditor in a personal workspace", e.staff, map[uuid.UUID]string{mine.Trigger.ID: "", fromNowhere.Trigger.ID: ""})
	check("a platform admin in a personal workspace", admin, map[uuid.UUID]string{mine.Trigger.ID: "", fromNowhere.Trigger.ID: ""})
}

// A chain of reminders, each run setting the next a minute ahead, is
// bounded: the agent's own schedules start at most TRIGGERS_AGENT_DAILY_RUNS
// runs a UTC day; beyond it a fire is skipped (and a one-time reminder
// removed, ending the chain). The next day counts afresh.
func TestAChainOfRemindersIsBounded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.allowSchedules(true)
	e.cfg.AgentDailyRuns = 3
	svc := e.replica()
	e.thread(e.agentID, "chat1", "shared", nil)
	chain := func() (sent bool, id uuid.UUID) {
		t.Helper()
		before := len(e.runner.sends())
		at := e.clock.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		made, err := svc.AgentCreate(ctx, e.agentID, triggers.AgentScheduleInput{At: at, Message: "Do the next step, then remind yourself", ThreadID: "chat1"})
		if err != nil {
			t.Fatalf("the reminder: %v", err)
		}
		e.clock.Add(time.Minute + 5*time.Second)
		e.tick(svc)
		e.replyEnds("chat1", false)
		e.clock.Add(time.Second)
		e.tick(svc)
		return len(e.runner.sends()) > before, made.Trigger.ID
	}
	for i := 0; i < 3; i++ {
		if sent, _ := chain(); !sent {
			t.Fatalf("run %d of the chain wasn't sent", i+1)
		}
	}
	sent, id := chain()
	if sent {
		t.Fatal("the chain went past the daily runs")
	}
	if n := e.scalar(`SELECT count(*)::text FROM agent_triggers WHERE id = $1`, id); n != "0" {
		t.Fatal("the skipped reminder is still there")
	}
	wantStatus(t, e.runs()[0], triggers.StatusSkipped, triggers.ReasonAgentDailyRuns)
	// The next UTC day: the agent's schedules run again.
	e.clock.Set(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	if sent, _ := chain(); !sent {
		t.Fatal("the next day's reminder wasn't sent")
	}
}
