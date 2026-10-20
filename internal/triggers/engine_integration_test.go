package triggers_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// A schedule fires at its time, once, delivers its message with the fire's
// ID as requestId, and its run ends when the thread's reply does.
func TestScheduleFiresOnTime(t *testing.T) {
	e := newEnv(t)
	trig := e.schedule("Five-minutely", "*/5 * * * *", triggers.MissedSkip)
	if got := trig.Trigger.NextFireAt; got == nil || !got.Equal(time.Date(2026, 10, 20, 10, 5, 0, 0, time.UTC)) {
		t.Fatalf("next fire = %v", got)
	}
	e.clock.Set(time.Date(2026, 10, 20, 10, 4, 59, 0, time.UTC))
	e.tick(e.svc)
	if n := len(e.runs()); n != 0 {
		t.Fatalf("%d runs before the time", n)
	}
	e.clock.Set(time.Date(2026, 10, 20, 10, 5, 3, 0, time.UTC))
	e.tick(e.svc)
	e.tick(e.svc) // nothing more at the same time
	runs := e.runs()
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	run := runs[0]
	wantStatus(t, run, triggers.StatusRunning, "")
	if !run.Run.ScheduledFor.Equal(time.Date(2026, 10, 20, 10, 5, 0, 0, time.UTC)) || run.Run.FiredBy != triggers.FiredBySchedule {
		t.Errorf("run = %+v", run.Run)
	}
	sends := e.runner.sends()
	if len(sends) != 1 || sends[0].RequestID != run.Run.FireID || sends[0].Text != "Report for 2026-10-20T10:05:00Z" ||
		sends[0].TriggerName != "Five-minutely" || sends[0].ThreadID != *run.Run.ThreadID {
		t.Fatalf("sent %+v", sends)
	}
	if !strings.HasPrefix(*run.Path, "/teams/acme/agents/"+e.agentID.String()+"?tab=test&thread=t_") {
		t.Errorf("link = %s", *run.Path)
	}
	// Costs while it runs: the agent's usage since it started.
	e.clock.Add(10 * time.Second)
	e.usage(50_000) // $0.50
	e.tick(e.svc)
	if got := e.run(run.Run.ID).Run.CostUsd; got < 0.49 || got > 0.51 {
		t.Errorf("cost in progress = %v", got)
	}
	e.clock.Add(20 * time.Second)
	e.replyEnds(*run.Run.ThreadID, false)
	e.tick(e.svc)
	done := e.run(run.Run.ID)
	wantStatus(t, done, triggers.StatusSucceeded, "")
	if done.Run.FinishedAt == nil || done.Run.CostUsd < 0.49 || done.Run.Requests != 1 {
		t.Errorf("finished run = %+v", done.Run)
	}
	// The next fire is the schedule's next time; every fire is audited, naming the trigger.
	got, _ := e.svc.Get(context.Background(), e.owner, e.agentID, trig.Trigger.ID)
	if !got.Trigger.NextFireAt.Equal(time.Date(2026, 10, 20, 10, 10, 0, 0, time.UTC)) {
		t.Errorf("next fire after = %v", got.Trigger.NextFireAt)
	}
	if a := e.audits("trigger.run"); len(a) != 1 || !strings.Contains(a[0], `"triggerName": "Five-minutely"`) || !strings.Contains(a[0], `"firedBy": "schedule"`) {
		t.Errorf("audit = %v", a)
	}
}

// Two replicas at the same moment: one run, delivered once, with or
// without the loop's lock (the fire ID decides).
func TestScheduleFiresOnceAcrossReplicas(t *testing.T) {
	e := newEnv(t)
	e.schedule("Every minute", "* * * * *", triggers.MissedSkip)
	replicas := []*triggers.Service{e.svc, e.replica(), e.replica()}
	for _, minute := range []int{2, 3, 4} {
		e.clock.Set(time.Date(2026, 10, 20, 10, minute, 1, 0, time.UTC))
		var wg sync.WaitGroup
		for i, r := range replicas {
			if i == 2 {
				r.SetLocker(nil) // a replica that doesn't take the loop's lock
			}
			for range 3 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := r.Tick(context.Background()); err != nil {
						t.Error(err)
					}
				}()
			}
		}
		wg.Wait()
		// Each run's reply ends, so the next minute's isn't skipped for concurrency.
		for _, r := range e.runs() {
			if r.Run.ThreadID != nil {
				e.replyEnds(*r.Run.ThreadID, false)
			}
		}
		e.tick(e.svc)
	}
	runs := e.runs()
	if len(runs) != 3 {
		t.Fatalf("%d runs for 3 minutes", len(runs))
	}
	fires := map[string]int{}
	for _, s := range e.runner.sends() {
		fires[s.RequestID]++
	}
	for _, r := range runs {
		if fires[r.Run.FireID] != 1 {
			t.Errorf("run %s delivered %d times", r.Run.FireID, fires[r.Run.FireID])
		}
		wantStatus(t, r, triggers.StatusSucceeded, "")
	}
}

// A schedule missed while the platform was down: skip records that it
// happened and runs nothing; run_once runs once for the latest time missed.
func TestMissedFirePolicies(t *testing.T) {
	e := newEnv(t)
	e.setLimits(triggers.Settings{MaxConcurrentRuns: ptr(int32(5))})
	skip := e.schedule("Hourly skip", "0 * * * *", triggers.MissedSkip)
	once := e.schedule("Hourly once", "0 * * * *", triggers.MissedRunOnce)
	// Down from 10:01 to 13:20: 11:00, 12:00 and 13:00 were missed.
	e.clock.Set(time.Date(2026, 10, 20, 13, 20, 0, 0, time.UTC))
	e.tick(e.svc)
	byTrigger := map[string]triggers.RunView{}
	for _, r := range e.runs() {
		if _, dup := byTrigger[r.Run.TriggerName]; dup {
			t.Fatalf("more than one run for %s", r.Run.TriggerName)
		}
		byTrigger[r.Run.TriggerName] = r
	}
	latest := time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)
	s, o := byTrigger["Hourly skip"], byTrigger["Hourly once"]
	wantStatus(t, s, triggers.StatusSkipped, triggers.ReasonMissed)
	wantStatus(t, o, triggers.StatusRunning, "")
	if !s.Run.ScheduledFor.Equal(latest) || !o.Run.ScheduledFor.Equal(latest) {
		t.Errorf("scheduled for %v and %v, want %v", s.Run.ScheduledFor, o.Run.ScheduledFor, latest)
	}
	if sends := e.runner.sends(); len(sends) != 1 || sends[0].TriggerName != "Hourly once" {
		t.Errorf("sent %+v", sends)
	}
	for _, id := range []string{skip.Trigger.ID.String(), once.Trigger.ID.String()} {
		next := e.scalar(`SELECT next_fire_at::text FROM agent_triggers WHERE id = $1`, id)
		if !strings.HasPrefix(next, "2026-10-20 14:00:00") {
			t.Errorf("next fire of %s = %s, want 14:00", id, next)
		}
	}
	// A late tick within the grace is on time, not missed.
	e.replyEnds(*o.Run.ThreadID, false)
	e.clock.Set(time.Date(2026, 10, 20, 14, 0, 40, 0, time.UTC))
	e.tick(e.svc)
	for _, r := range e.runs() {
		if r.Run.ScheduledFor.Equal(time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)) && r.Run.Status == triggers.StatusSkipped {
			t.Errorf("a fire 40 s late counted as missed: %+v", r.Run)
		}
	}
}

// A webhook delivery: a valid signature starts a run; a wrong, stale or
// missing one is refused; a replay gets the first run back and starts
// nothing; a delivery the filter doesn't match starts nothing.
func TestWebhookSignaturesReplayAndFilter(t *testing.T) {
	e := newEnv(t)
	saved := e.webhook("Issues", triggers.Input{Filter: &triggers.Filter{Path: "action", Equals: "opened"}})
	if !strings.HasPrefix(saved.WebhookSecret, "pdpwh_") || !strings.HasPrefix(saved.WebhookURL, "https://pdp.example.com/v1/hooks/h_") {
		t.Fatalf("saved = %+v", saved)
	}
	body := `{"action":"opened","title":"Login is broken"}`
	res, err := e.deliver(e.svc, saved, body)
	if err != nil || !res.Matched || res.Replayed || res.Run.Status != triggers.StatusPending {
		t.Fatalf("delivery = %+v, %v", res, err)
	}
	if sends := e.runner.sends(); len(sends) != 1 || sends[0].Text != "Issue: Login is broken" || sends[0].RequestID != res.Run.FireID {
		t.Fatalf("sent %+v", sends)
	}
	// The same delivery again (a replay, or the sender retrying): the same run, nothing new.
	replay, err := e.deliver(e.svc, saved, body)
	if err != nil || !replay.Replayed || replay.Run.ID != res.Run.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	// Another replica sees it too.
	if replay, err := e.deliver(e.replica(), saved, body); err != nil || !replay.Replayed || replay.Run.ID != res.Run.ID {
		t.Fatalf("replay on a replica = %+v, %v", replay, err)
	}
	if n := len(e.runner.sends()); n != 1 {
		t.Fatalf("%d messages after replays", n)
	}
	hook := hookID(saved.View)
	now := e.clock.Now()
	for name, sig := range map[string]string{
		"wrong secret": triggers.Sign("pdpwh_not-the-secret", now, []byte(body)),
		"other body":   triggers.Sign(saved.WebhookSecret, now, []byte(`{"action":"opened","title":"x"}`)),
		"stale":        triggers.Sign(saved.WebhookSecret, now.Add(-6*time.Minute), []byte(body)),
		"future":       triggers.Sign(saved.WebhookSecret, now.Add(6*time.Minute), []byte(body)),
		"missing":      "",
	} {
		if _, err := e.svc.Webhook(context.Background(), hook, sig, []byte(body)); !isCode(err, "invalid_signature") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.svc.Webhook(context.Background(), "h_nope", triggers.Sign(saved.WebhookSecret, now, []byte(body)), []byte(body)); !isCode(err, "hook_not_found") {
		t.Errorf("unknown address: %v", err)
	}
	// The filter: another action, or a body that isn't JSON.
	for _, b := range []string{`{"action":"closed","title":"x"}`, `not json`} {
		e.clock.Add(time.Second)
		if res, err := e.deliver(e.svc, saved, b); err != nil || res.Matched {
			t.Errorf("filtered delivery %s = %+v, %v", b, res, err)
		}
	}
	if n := len(e.runs()); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
	// A rotated secret: the old one stops working at once.
	rotated, err := e.svc.RotateSecret(context.Background(), e.owner, e.agentID, saved.Trigger.ID, triggers.SecretWebhook)
	if err != nil || rotated.WebhookSecret == "" || rotated.WebhookSecret == saved.WebhookSecret {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	e.clock.Add(time.Second)
	if _, err := e.deliver(e.svc, saved, body); !isCode(err, "invalid_signature") {
		t.Errorf("the old secret after rotation: %v", err)
	}
}

// The same fire delivered twice (a lost answer, a replica that died while
// delivering) runs once: the retry carries the same requestId to the same
// thread.
func TestDeliveryRetryIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.runner.fail, e.runner.failN = &agents.TriggerError{Code: "agent_unavailable"}, 1
	saved := e.webhook("Retry", triggers.Input{})
	res, err := e.deliver(e.svc, saved, `{"title":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.run(res.Run.ID), triggers.StatusPending, "")
	// Retried after its back-off, by any replica's loop.
	e.clock.Add(30 * time.Second)
	e.tick(e.replica())
	e.tick(e.svc)
	wantStatus(t, e.run(res.Run.ID), triggers.StatusRunning, "")
	sends := e.runner.sends()
	if len(sends) != 2 || sends[0].RequestID != sends[1].RequestID || sends[0].ThreadID != sends[1].ThreadID {
		t.Fatalf("attempts %+v", sends)
	}
	if e.runner.accepted[res.Run.FireID] != 1 {
		t.Errorf("the runtime ran the fire %d times", e.runner.accepted[res.Run.FireID])
	}
	// A permanent refusal fails the run at once.
	e.runner.fail, e.runner.failN = &agents.TriggerError{Code: "thread_private", Permanent: true}, 1
	e.replyEnds(sends[0].ThreadID, false)
	e.tick(e.svc)
	e.clock.Add(time.Second)
	res2, _ := e.deliver(e.svc, saved, `{"title":"y"}`)
	wantStatus(t, e.run(res2.Run.ID), triggers.StatusFailed, "thread_private")
}
