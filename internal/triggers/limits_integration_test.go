package triggers_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/teamnet"
	"github.com/ncecere/pi-durable-platform/internal/triggers"
)

// Runs at once: a fire while the agent has as many runs as it may is
// skipped (concurrency); the limit counts pending and running runs.
func TestConcurrencyLimit(t *testing.T) {
	e := newEnv(t)
	saved := e.webhook("Burst", triggers.Input{})
	first, _ := e.deliver(e.svc, saved, `{"title":"1"}`)
	e.clock.Add(time.Second)
	second, _ := e.deliver(e.svc, saved, `{"title":"2"}`)
	wantStatus(t, e.run(first.Run.ID), triggers.StatusRunning, "")
	wantStatus(t, e.run(second.Run.ID), triggers.StatusSkipped, triggers.ReasonConcurrency)
	// Raised to two: the next fire runs beside the first.
	e.setLimits(triggers.Settings{MaxConcurrentRuns: ptr(int32(2))})
	e.clock.Add(time.Second)
	third, _ := e.deliver(e.svc, saved, `{"title":"3"}`)
	wantStatus(t, e.run(third.Run.ID), triggers.StatusRunning, "")
	// Above the platform's ceiling is refused.
	cur, _ := e.svc.GetSettings(context.Background(), e.owner, e.agentID)
	if _, err := e.svc.SetSettings(context.Background(), e.owner, e.agentID, triggers.Settings{MaxConcurrentRuns: ptr(int32(6))}, cur.Revision); !isCode(err, "invalid_trigger_limits") {
		t.Errorf("above the ceiling: %v", err)
	}
	// Concurrent deliveries can't get past the limit together (the agent's run lock).
	e.setLimits(triggers.Settings{MaxConcurrentRuns: ptr(int32(3))})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := `{"title":"c` + string(rune('a'+i)) + `"}`
			sig := triggers.Sign(saved.WebhookSecret, e.clock.Now(), []byte(body))
			if _, err := e.svc.Webhook(context.Background(), hookID(saved.View), sig, []byte(body)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	e.svc.Wait()
	if n := e.scalar(`SELECT count(*)::text FROM trigger_runs WHERE agent_id = $1 AND status IN ('pending', 'running')`, e.agentID); n != "3" {
		t.Errorf("%s runs at once, want 3", n)
	}
}

// A run past its maximum run time is stopped (its thread aborted), counts
// as a failure, and the trigger backs off; after the limit of failures in
// a row it pauses itself (audited). A success resets the count.
func TestRunTimeBackoffAndAutoPause(t *testing.T) {
	e := newEnv(t)
	e.setLimits(triggers.Settings{MaxRunSeconds: ptr(int32(120))})
	saved := e.webhook("Slow", triggers.Input{})
	res, _ := e.deliver(e.svc, saved, `{"title":"1"}`)
	e.clock.Add(119 * time.Second)
	e.tick(e.svc)
	wantStatus(t, e.run(res.Run.ID), triggers.StatusRunning, "")
	e.clock.Add(2 * time.Second)
	e.tick(e.svc)
	stopped := e.run(res.Run.ID)
	wantStatus(t, stopped, triggers.StatusStopped, triggers.ReasonMaxRunTime)
	if stops := e.runner.stops(); len(stops) != 1 || stops[0] != *stopped.Run.ThreadID {
		t.Errorf("stopped threads %v", stops)
	}
	if a := e.audits("trigger.run_stop"); len(a) != 1 || !strings.Contains(a[0], `"runReason": "max_run_time"`) || !strings.Contains(a[0], `"triggerName": "Slow"`) {
		t.Errorf("audit = %v", a)
	}
	// Backing off: a fire within the minute after the failure is skipped.
	e.clock.Add(30 * time.Second)
	backoff, _ := e.deliver(e.svc, saved, `{"title":"2"}`)
	wantStatus(t, e.run(backoff.Run.ID), triggers.StatusSkipped, triggers.ReasonBackoff)
	// Failed replies: the third failure in a row (PauseAfterFailures 3) pauses it.
	for i, wantPaused := range []bool{false, true} {
		e.clock.Add(2 * time.Hour)
		r, _ := e.deliver(e.svc, saved, `{"title":"f`+string(rune('a'+i))+`"}`)
		wantStatus(t, e.run(r.Run.ID), triggers.StatusRunning, "")
		e.clock.Add(time.Second)
		e.replyEnds(*e.run(r.Run.ID).Run.ThreadID, true)
		e.tick(e.svc)
		wantStatus(t, e.run(r.Run.ID), triggers.StatusFailed, triggers.ReasonReplyError)
		got, _ := e.svc.Get(context.Background(), e.owner, e.agentID, saved.Trigger.ID)
		if got.Trigger.Paused != wantPaused {
			t.Fatalf("after failure %d: paused %v", i+2, got.Trigger.Paused)
		}
	}
	got, _ := e.svc.Get(context.Background(), e.owner, e.agentID, saved.Trigger.ID)
	if got.Trigger.PausedReason == nil || *got.Trigger.PausedReason != "failures" || got.Trigger.ConsecutiveFailures != 3 {
		t.Errorf("paused trigger = %+v", got.Trigger)
	}
	if a := e.audits("trigger.auto_pause"); len(a) != 1 || !strings.Contains(a[0], `"failures": 3`) {
		t.Errorf("auto-pause audit = %v", a)
	}
	// Paused: a delivery is recorded as skipped.
	e.clock.Add(2 * time.Hour)
	p, _ := e.deliver(e.svc, saved, `{"title":"paused"}`)
	wantStatus(t, e.run(p.Run.ID), triggers.StatusSkipped, triggers.ReasonTriggerPaused)
	// Run now ignores the trigger's pause; resuming forgets the failures.
	run, err := e.svc.Fire(context.Background(), e.owner, e.agentID, saved.Trigger.ID)
	e.svc.Wait()
	if err != nil || run.FiredBy != triggers.FiredByManual {
		t.Fatalf("run now = %+v, %v", run, err)
	}
	wantStatus(t, e.run(run.ID), triggers.StatusRunning, "")
	if a := e.audits("trigger.fire"); len(a) != 1 {
		t.Errorf("run now audit = %v", a)
	}
	resumed, err := e.svc.Update(context.Background(), e.owner, e.agentID, saved.Trigger.ID, triggers.Input{Paused: ptr(false)}, got.Trigger.Revision)
	if err != nil || resumed.Trigger.Paused || resumed.Trigger.ConsecutiveFailures != 0 || resumed.Trigger.BackoffUntil != nil {
		t.Fatalf("resumed = %+v, %v", resumed.Trigger, err)
	}
	e.clock.Add(time.Second)
	e.replyEnds(*e.run(run.ID).Run.ThreadID, false)
	e.tick(e.svc)
	wantStatus(t, e.run(run.ID), triggers.StatusSucceeded, "")
}

// Budgets: a run whose trigger's spending today goes over its budget is
// stopped; the next fire that day is skipped. The agent's budget covers all
// its triggers' runs. A new UTC day starts afresh.
func TestBudgets(t *testing.T) {
	e := newEnv(t)
	e.setLimits(triggers.Settings{MaxConcurrentRuns: ptr(int32(5))})
	saved := e.webhook("Costly", triggers.Input{DailyBudgetUSD: ptr(1.0)})
	res, _ := e.deliver(e.svc, saved, `{"title":"1"}`)
	e.clock.Add(time.Second)
	e.usage(60_000) // $0.60
	e.tick(e.svc)
	wantStatus(t, e.run(res.Run.ID), triggers.StatusRunning, "")
	e.clock.Add(time.Second)
	e.usage(60_000) // $1.20 in all
	e.tick(e.svc)
	stopped := e.run(res.Run.ID)
	wantStatus(t, stopped, triggers.StatusStopped, triggers.ReasonBudget)
	if len(e.runner.stops()) != 1 || stopped.Run.CostUsd < 1.19 {
		t.Errorf("stops %v, cost %v", e.runner.stops(), stopped.Run.CostUsd)
	}
	// A budget stop is a limit, not the trigger's failure.
	got, _ := e.svc.Get(context.Background(), e.owner, e.agentID, saved.Trigger.ID)
	if got.Trigger.ConsecutiveFailures != 0 {
		t.Errorf("failures after a budget stop: %d", got.Trigger.ConsecutiveFailures)
	}
	e.clock.Add(time.Minute)
	next, _ := e.deliver(e.svc, saved, `{"title":"2"}`)
	wantStatus(t, e.run(next.Run.ID), triggers.StatusSkipped, triggers.ReasonBudget)
	// The agent's budget: another trigger of the agent, without a budget of its own.
	other := e.webhook("Other", triggers.Input{})
	e.setLimits(triggers.Settings{MaxConcurrentRuns: ptr(int32(5)), DailyBudgetUSD: ptr(1.0)})
	e.clock.Add(time.Second)
	o, _ := e.deliver(e.svc, other, `{"title":"o"}`)
	wantStatus(t, e.run(o.Run.ID), triggers.StatusSkipped, triggers.ReasonBudget)
	// Tomorrow (UTC) both may run again.
	e.clock.Set(time.Date(2026, 10, 21, 0, 0, 5, 0, time.UTC))
	tomorrow, _ := e.deliver(e.svc, saved, `{"title":"3"}`)
	wantStatus(t, e.run(tomorrow.Run.ID), triggers.StatusRunning, "")
}

// Pausing an agent's triggers: schedules move on without runs (nothing
// missed piles up), webhook deliveries are recorded as skipped.
func TestAgentPause(t *testing.T) {
	e := newEnv(t)
	e.schedule("Every minute", "* * * * *", triggers.MissedRunOnce)
	saved := e.webhook("Hook", triggers.Input{})
	e.setLimits(triggers.Settings{Paused: true})
	for range 3 {
		e.clock.Add(time.Minute)
		e.tick(e.svc)
	}
	res, _ := e.deliver(e.svc, saved, `{"title":"x"}`)
	runs := e.runs()
	if len(runs) != 1 {
		t.Fatalf("%d runs while paused, want the webhook's alone", len(runs))
	}
	wantStatus(t, e.run(res.Run.ID), triggers.StatusSkipped, triggers.ReasonAgentPaused)
	// Resumed: the schedule runs at its next minute, not for the minutes it was paused.
	e.setLimits(triggers.Settings{Paused: false})
	e.clock.Add(time.Minute)
	e.tick(e.svc)
	if sends := e.runner.sends(); len(sends) != 1 {
		t.Errorf("%d messages after resuming, want 1", len(sends))
	}
}

// The completion callback: signed with the trigger's callback secret, sent
// through the address guard, carrying the run's summary and an excerpt,
// never the message or the transcript; retried while the receiver fails.
func TestCompletionCallback(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var got []*http.Request
	var bodies [][]byte
	fail := 1
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		got, bodies = append(got, r), append(bodies, b)
		if fail > 0 {
			fail--
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	hosts := map[string]string{"hooks.example.com": "93.184.216.34", "internal.example.com": "10.1.2.3"}
	e.svc.SetTeamNetwork(teamnet.Config{
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(hosts[host])}, nil
		},
		Connect: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLS: &tls.Config{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, ServerName: "example.com"},
	})
	// The guard: a private address (not in the allowed networks) and plain http are refused when saved.
	name := "Bad"
	for url, code := range map[string]string{"https://internal.example.com/cb": "address_not_allowed", "http://hooks.example.com/cb": "invalid_callback_url"} {
		_, err := e.svc.Create(context.Background(), e.owner, e.agentID, triggers.Input{Kind: triggers.KindWebhook, Name: &name,
			MessageTemplate: ptr("x"), CallbackURL: &url})
		if !isCode(err, code) {
			t.Errorf("callback %s: %v", url, err)
		}
	}
	saved := e.webhook("Reported", triggers.Input{CallbackURL: ptr("https://hooks.example.com/cb"),
		MessageTemplate: ptr("SECRET-TASK {{payload.title}}")})
	if !strings.HasPrefix(saved.CallbackSecret, "pdpcb_") || saved.Trigger.CallbackSecretPrefix == nil {
		t.Fatalf("saved = %+v", saved)
	}
	res, _ := e.deliver(e.svc, saved, `{"title":"Payload text"}`)
	e.clock.Add(time.Second)
	e.replyEnds(*e.run(res.Run.ID).Run.ThreadID, false)
	e.tick(e.svc) // finishes the run and sends the callback: 503
	if v := e.run(res.Run.ID); v.Run.CallbackStatus == nil || *v.Run.CallbackStatus != "pending" {
		t.Fatalf("callback after a failure = %v", v.Run.CallbackStatus)
	}
	e.clock.Add(31 * time.Second)
	e.tick(e.svc)
	if v := e.run(res.Run.ID); v.Run.CallbackStatus == nil || *v.Run.CallbackStatus != "delivered" {
		t.Fatalf("callback = %v", v.Run.CallbackStatus)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("%d callbacks", len(got))
	}
	req, body := got[1], bodies[1]
	if req.Header.Get("X-PDP-Event") != "run.finished" || req.Header.Get("X-PDP-Delivery") != res.Run.ID.String() {
		t.Errorf("headers %v", req.Header)
	}
	// The receiver checks the signature with its callback secret.
	sig := req.Header.Get(triggers.SignatureHeader)
	ts := strings.TrimPrefix(strings.Split(sig, ",")[0], "t=")
	want := triggers.Sign(saved.CallbackSecret, time.Unix(mustInt(t, ts), 0), body)
	if sig != want {
		t.Errorf("signature %s, want %s", sig, want)
	}
	var cb triggers.Callback
	if err := json.Unmarshal(body, &cb); err != nil {
		t.Fatal(err)
	}
	if cb.RunID != res.Run.ID || cb.Status != triggers.StatusSucceeded || cb.Excerpt == nil || *cb.Excerpt != "All done." ||
		cb.Link == nil || !strings.HasPrefix(*cb.Link, "https://pdp.example.com/teams/acme/agents/") || cb.TriggerName != "Reported" {
		t.Errorf("callback = %s", body)
	}
	for _, leak := range []string{"SECRET-TASK", "Payload text", saved.CallbackSecret, saved.WebhookSecret} {
		if strings.Contains(string(body), leak) {
			t.Errorf("the callback carries %q: %s", leak, body)
		}
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("not a number: %s", s)
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// Who may do what: owners and admins manage, in a browser session; editors,
// members and service accounts read; platform staff read; nobody else sees
// anything. Every change is audited without secrets.
func TestAuthorizationAndAudit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := func() triggers.Input {
		return triggers.Input{Kind: triggers.KindSchedule, Name: ptr("Daily"), Cron: ptr("@daily"), MessageTemplate: ptr("Go")}
	}
	key := e.owner
	key.APIKeyID = e.agentID // a personal API key
	app := e.admin
	app.OAuthGrantID = e.agentID // an app the admin connected
	for name, c := range map[string]struct {
		a    func() (err error)
		code string
	}{
		"editor":          {func() error { _, err := e.svc.Create(ctx, e.editor, e.agentID, in()); return err }, "forbidden"},
		"member":          {func() error { _, err := e.svc.Create(ctx, e.member, e.agentID, in()); return err }, "forbidden"},
		"service account": {func() error { _, err := e.svc.Create(ctx, e.service, e.agentID, in()); return err }, "forbidden"},
		"api key":         {func() error { _, err := e.svc.Create(ctx, key, e.agentID, in()); return err }, "forbidden"},
		"app":             {func() error { _, err := e.svc.Create(ctx, app, e.agentID, in()); return err }, "forbidden"},
		"staff":           {func() error { _, err := e.svc.Create(ctx, e.staff, e.agentID, in()); return err }, "agent_not_found"},
		"outsider":        {func() error { _, err := e.svc.Create(ctx, e.outsider, e.agentID, in()); return err }, "agent_not_found"},
	} {
		if err := c.a(); !isCode(err, c.code) {
			t.Errorf("%s creating: %v, want %s", name, err, c.code)
		}
	}
	saved, err := e.svc.Create(ctx, e.admin, e.agentID, in())
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]bool{"owner": true, "editor": true, "member": true, "service": true, "staff": true, "outsider": false} {
		actor := map[string]authz.Actor{"owner": e.owner, "editor": e.editor, "member": e.member, "service": e.service, "staff": e.staff, "outsider": e.outsider}[name]
		_, err := e.svc.List(ctx, actor, e.agentID)
		if (err == nil) != a {
			t.Errorf("%s listing: %v", name, err)
		}
		_, err = e.svc.Runs(ctx, actor, e.agentID, triggers.RunPage{Limit: 10})
		if (err == nil) != a {
			t.Errorf("%s listing runs: %v", name, err)
		}
	}
	if _, err := e.svc.Fire(ctx, e.editor, e.agentID, saved.Trigger.ID); !isCode(err, "forbidden") {
		t.Errorf("editor run now: %v", err)
	}
	if err := e.svc.Delete(ctx, e.member, e.agentID, saved.Trigger.ID); !isCode(err, "forbidden") {
		t.Errorf("member delete: %v", err)
	}
	// Service-managed agents take no triggers: their people may not use them.
	managed := e.newAgent("app-user")
	e.exec(`UPDATE agents SET external_key = 'user-1', service_managed = true WHERE id = $1`, managed)
	if _, err := e.svc.Create(ctx, e.owner, managed, in()); !isCode(err, "agent_not_found") {
		t.Errorf("a trigger on a service-managed agent: %v", err)
	}
	// An archived team's triggers are read-only.
	e.exec(`UPDATE teams SET status = 'archived', archived_at = now() WHERE id = $1`, e.teamID)
	if _, err := e.svc.Create(ctx, e.owner, e.agentID, in()); !isCode(err, "team_archived") {
		t.Errorf("archived team: %v", err)
	}
	e.exec(`UPDATE teams SET status = 'active', archived_at = NULL WHERE id = $1`, e.teamID)
	upd, err := e.svc.Update(ctx, e.owner, e.agentID, saved.Trigger.ID, triggers.Input{Name: ptr("Nightly")}, saved.Trigger.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(ctx, e.owner, e.agentID, saved.Trigger.ID, triggers.Input{Name: ptr("Stale")}, saved.Trigger.Revision); !isCode(err, "revision_conflict") {
		t.Errorf("a stale revision: %v", err)
	}
	if err := e.svc.Delete(ctx, e.owner, e.agentID, upd.Trigger.ID); err != nil {
		t.Fatal(err)
	}
	actions := e.scalar(`SELECT string_agg(action, ',' ORDER BY id) FROM audit_log WHERE target_type = 'trigger'`)
	if actions != "trigger.create,trigger.update,trigger.delete" {
		t.Errorf("audited %s", actions)
	}
	if s := e.scalar(`SELECT count(*)::text FROM audit_log WHERE action = 'trigger_settings.update'`); s != "0" {
		t.Errorf("settings audits %s", s)
	}
	// Secrets never reach the audit log.
	wh := e.webhook("Hook", triggers.Input{CallbackURL: nil})
	e.setLimits(triggers.Settings{Paused: false})
	if n := e.scalar(`SELECT count(*)::text FROM audit_log WHERE before_state::text LIKE '%pdpwh_%' OR after_state::text LIKE '%pdpwh_%' OR metadata::text LIKE '%pdpwh_%'`); n != "0" || wh.WebhookSecret == "" {
		t.Errorf("%s audit entries hold a secret", n)
	}
	if a := e.audits("trigger_settings.update"); len(a) != 1 {
		t.Errorf("settings audits %v", a)
	}
}

// A run whose thread waits for a person's approval of a tool call isn't
// stopped for its run time: the approval's own expiry bounds the wait
// (docs/approvals.md), and the run goes on once it is decided.
func TestARunWaitingForAnApprovalIsNotStoppedForItsRunTime(t *testing.T) {
	e := newEnv(t)
	e.setLimits(triggers.Settings{MaxRunSeconds: ptr(int32(120))})
	saved := e.webhook("Careful", triggers.Input{})
	res, _ := e.deliver(e.svc, saved, `{"title":"1"}`)
	run := e.run(res.Run.ID)
	e.scalar(`INSERT INTO tool_approvals (agent_id, id, thread_id, tool, call_id, expires_at)
		VALUES ($1, 'ap_waiting', $2, 'mcp__crm__delete', 'c1', now() + interval '1 day') RETURNING id`, e.agentID, *run.Run.ThreadID)
	e.clock.Add(10 * time.Minute)
	e.tick(e.svc)
	wantStatus(t, e.run(res.Run.ID), triggers.StatusRunning, "")
	e.scalar(`UPDATE tool_approvals SET status = 'approved', decided_at = now() WHERE id = 'ap_waiting' RETURNING id`)
	e.tick(e.svc)
	wantStatus(t, e.run(res.Run.ID), triggers.StatusStopped, triggers.ReasonMaxRunTime)
}
