package triggers

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

var threadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Run statuses.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusStopped   = "stopped"
	StatusSkipped   = "skipped"
)

// How a run was fired.
const (
	FiredBySchedule = "schedule"
	FiredByWebhook  = "webhook"
	FiredByManual   = "manual"
)

// Why a run was skipped, stopped or failed (besides the runner's codes).
const (
	ReasonTriggerPaused = "trigger_paused"
	ReasonAgentPaused   = "agent_paused"
	ReasonTeamArchived  = "team_archived"
	ReasonConcurrency   = "concurrency"
	ReasonBudget        = "budget"
	ReasonBackoff       = "backoff"
	ReasonMissed        = "missed"
	ReasonMaxRunTime    = "max_run_time"
	ReasonReplyError    = "reply_error"
	ReasonThreadDeleted = "thread_deleted"
	ReasonDelivery      = "delivery_failed"
)

const (
	// deliveryLease is how long a replica has to deliver a run before
	// another may try (longer than a cold start).
	deliveryLease = 3 * time.Minute
	// deliveryAttempts and deliveryWindow bound retries of a delivery.
	deliveryAttempts = 5
	deliveryWindow   = 15 * time.Minute
)

// limits are an agent's limits for its triggered runs, as they apply now.
type limits struct {
	paused        bool
	maxConcurrent int64
	maxRun        time.Duration
	budget        *float64
	revision      int64
}

func (s *Service) limitsOf(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID) (limits, error) {
	l := limits{maxConcurrent: 1, maxRun: s.cfg.MaxRunTime, revision: 1}
	row, err := q.GetTriggerSettings(ctx, agentID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return l, nil
	} else if err != nil {
		return l, err
	}
	l.paused, l.revision = row.Paused, row.Revision
	if row.MaxConcurrentRuns != nil {
		l.maxConcurrent = int64(*row.MaxConcurrentRuns)
	}
	if row.MaxRunSeconds != nil {
		l.maxRun = time.Duration(*row.MaxRunSeconds) * time.Second
	}
	if row.DailyBudgetUsd.Valid {
		b := row.DailyBudgetUsd.Float64
		l.budget = &b
	}
	// The platform's ceilings hold whatever was stored under older ones.
	l.maxConcurrent = min(l.maxConcurrent, int64(s.cfg.MaxConcurrentRuns))
	l.maxRun = min(l.maxRun, s.cfg.MaxRunTime)
	return l, nil
}

// dayStart is the start of now's UTC day (budgets are per UTC day).
func dayStart(now time.Time) time.Time {
	y, m, d := now.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// fire is one fire of a trigger, recorded in the caller's transaction.
type fire struct {
	trig    dbgen.AgentTrigger
	teamID  uuid.UUID
	firedBy string
	fireID  string
	at      *time.Time // the scheduled time
	message string
	// skip: recorded as skipped for this reason, whatever the limits say.
	skip string
	// manual: a person's Run now (ignores the trigger's pause and back-off).
	manual *authz.Actor
}

// record records a fire's run (or finds the run of an earlier fire with the
// same ID: replayed), checking the limits under the agent's run lock: a fire
// that breaks one is recorded as skipped. Call it in a transaction whose
// first lock was the trigger's row (LockTrigger, or DueSchedules), so locks
// are always taken in the same order. The run, when pending, is delivered
// after the commit (deliverSoon).
func (s *Service) record(ctx context.Context, q *dbgen.Queries, f fire, now time.Time) (dbgen.TriggerRun, bool, error) {
	if err := q.LockAgentRuns(ctx, f.trig.AgentID); err != nil {
		return dbgen.TriggerRun{}, false, err
	}
	if prev, err := q.GetRunByFireID(ctx, f.fireID); err == nil {
		return prev, true, nil
	} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
		return dbgen.TriggerRun{}, false, err
	}
	reason := f.skip
	if reason == "" {
		var err error
		if reason, err = s.skipReason(ctx, q, f, now); err != nil {
			return dbgen.TriggerRun{}, false, err
		}
	}
	p := dbgen.InsertRunParams{FireID: f.fireID, TriggerID: uuid.NullUUID{UUID: f.trig.ID, Valid: true}, AgentID: f.trig.AgentID,
		TeamID: f.teamID, TriggerName: f.trig.Name, TriggerKind: f.trig.Kind, FiredBy: f.firedBy, Status: StatusPending,
		ScheduledFor: f.at, Now: now}
	if reason != "" {
		p.Status, p.Reason, p.FinishedAt = StatusSkipped, &reason, &now
	} else {
		thread := newThreadID()
		if f.trig.ThreadID != nil {
			thread = *f.trig.ThreadID
		}
		p.ThreadID = &thread
	}
	run, err := q.InsertRun(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) { // a concurrent fire with the same ID won
		run, err = q.GetRunByFireID(ctx, f.fireID)
		return run, true, err
	} else if err != nil {
		return run, false, err
	}
	if run.Status == StatusPending {
		sealed, err := s.seal(f.message, runAD(run.ID))
		if err != nil {
			return run, false, err
		}
		if err := q.SetRunMessage(ctx, dbgen.SetRunMessageParams{ID: run.ID, MessageEnc: sealed}); err != nil {
			return run, false, err
		}
	}
	if err := q.TriggerFired(ctx, dbgen.TriggerFiredParams{ID: f.trig.ID, At: now}); err != nil {
		return run, false, err
	}
	var e audit.Entry
	if f.manual != nil {
		e = f.manual.Audit("trigger.fire", "trigger_run", run.ID.String())
	} else {
		e = audit.Entry{ActorKind: audit.ActorSystem, Action: "trigger.run", TargetType: "trigger_run", TargetID: run.ID.String()}
	}
	e.TeamID = f.teamID
	meta := map[string]any{"triggerId": f.trig.ID.String(), "triggerName": f.trig.Name, "agentId": f.trig.AgentID.String(),
		"firedBy": f.firedBy, "status": run.Status, "fireId": run.FireID}
	if run.ThreadID != nil {
		meta["threadId"] = *run.ThreadID
	}
	if run.Reason != nil {
		meta["runReason"] = *run.Reason
	}
	e.Metadata = withMeta(e.Metadata, meta)
	return run, false, audit.Record(ctx, q, e)
}

// skipReason is the limit a fire breaks now ("" for none).
func (s *Service) skipReason(ctx context.Context, q *dbgen.Queries, f fire, now time.Time) (string, error) {
	l, err := s.limitsOf(ctx, q, f.trig.AgentID)
	if err != nil {
		return "", err
	}
	switch {
	case l.paused:
		return ReasonAgentPaused, nil
	case f.manual == nil && f.trig.Paused:
		return ReasonTriggerPaused, nil
	case f.manual == nil && f.trig.BackoffUntil != nil && now.Before(*f.trig.BackoffUntil):
		return ReasonBackoff, nil
	}
	active, err := q.ActiveRunCount(ctx, f.trig.AgentID)
	if err != nil {
		return "", err
	}
	if active >= l.maxConcurrent {
		return ReasonConcurrency, nil
	}
	since := dayStart(now)
	if f.trig.DailyBudgetUsd.Valid {
		spent, err := q.TriggerCostSince(ctx, dbgen.TriggerCostSinceParams{TriggerID: uuid.NullUUID{UUID: f.trig.ID, Valid: true}, Since: since})
		if err != nil {
			return "", err
		}
		if spent >= f.trig.DailyBudgetUsd.Float64 {
			return ReasonBudget, nil
		}
	}
	if l.budget != nil {
		spent, err := q.AgentRunCostSince(ctx, dbgen.AgentRunCostSinceParams{AgentID: f.trig.AgentID, Since: since})
		if err != nil {
			return "", err
		}
		if spent >= *l.budget {
			return ReasonBudget, nil
		}
	}
	return "", nil
}

// deliverSoon delivers a pending run in the background (the request that
// fired it doesn't wait for the agent to wake). If this replica fails to, the
// trigger loop of any replica delivers it after the lease.
func (s *Service) deliverSoon(run dbgen.TriggerRun) {
	if run.Status != StatusPending {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), deliveryLease)
		defer cancel()
		s.deliver(ctx, run.ID)
	}()
}

// deliver takes the lease on a pending run and sends its message. A
// permanent refusal, or too many attempts, fails the run; otherwise it is
// tried again later.
func (s *Service) deliver(ctx context.Context, id uuid.UUID) {
	now := s.now()
	run, err := s.q.ClaimRunDelivery(ctx, dbgen.ClaimRunDeliveryParams{ID: id, Now: now, Lease: now.Add(deliveryLease)})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("triggers: claim a run's delivery", "run", id, "err", err)
		}
		return // delivered, finished, or another replica has the lease
	}
	text, err := s.box.Open(run.MessageEnc, []byte(runAD(run.ID)))
	if err != nil || run.ThreadID == nil {
		s.log.Warn("triggers: a pending run's message can't be read", "run", id)
		s.finish(ctx, run, StatusFailed, ReasonDelivery, now)
		return
	}
	err = s.runner.SendTriggered(ctx, agents.TriggerMessage{AgentID: run.AgentID, ThreadID: *run.ThreadID, Text: string(text),
		RequestID: run.FireID, TriggerID: run.TriggerID.UUID, TriggerName: run.TriggerName, At: now})
	if err == nil {
		if _, err := s.q.RunStarted(context.WithoutCancel(ctx), dbgen.RunStartedParams{ID: run.ID, At: now}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.log.Warn("triggers: record a run's start", "run", id, "err", err)
		}
		return
	}
	var te *agents.TriggerError
	code := ReasonDelivery
	if errors.As(err, &te) {
		code = te.Code
	}
	if (te != nil && te.Permanent) || run.Attempts >= deliveryAttempts || now.Sub(run.CreatedAt) >= deliveryWindow {
		s.log.Info("triggers: run not delivered", "run", id, "agent", run.AgentID, "reason", code)
		s.finish(ctx, run, StatusFailed, code, now)
		return
	}
	retry := now.Add(min(10*time.Second<<(run.Attempts-1), 2*time.Minute))
	if err := s.q.RunRetryAt(context.WithoutCancel(ctx), dbgen.RunRetryAtParams{ID: run.ID, At: retry}); err != nil {
		s.log.Warn("triggers: schedule a delivery retry", "run", id, "err", err)
	}
}

// failure reports whether a run's end counts against its trigger (back-off,
// automatic pause).
func failure(status, reason string) bool {
	return status == StatusFailed || (status == StatusStopped && reason == ReasonMaxRunTime)
}

// finish ends a run (unless another replica already did): its cost, its
// callback, its trigger's failures in a row (back-off, and a pause at the
// limit), and the audit entries of a stop or an automatic pause.
func (s *Service) finish(ctx context.Context, run dbgen.TriggerRun, status, reason string, now time.Time) {
	ctx = context.WithoutCancel(ctx)
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		var cost float64
		var requests int64
		if run.StartedAt != nil {
			u, err := q.AgentUsageBetween(ctx, dbgen.AgentUsageBetweenParams{AgentID: run.AgentID, Since: *run.StartedAt, Until: now.Add(time.Second)})
			if err != nil {
				return err
			}
			cost, requests = u.CostUsd, u.Requests
		}
		var trig *dbgen.AgentTrigger
		if run.TriggerID.Valid {
			t, err := q.GetTriggerByID(ctx, run.TriggerID.UUID)
			if err == nil {
				trig = &t
			} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
				return err
			}
		}
		var r *string
		if reason != "" {
			r = &reason
		}
		done, err := q.FinishRun(ctx, dbgen.FinishRunParams{ID: run.ID, Status: status, Reason: r, At: now, CostUsd: cost, Requests: requests,
			Callback: trig != nil && trig.CallbackUrl != nil})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already finished elsewhere
		} else if err != nil {
			return err
		}
		if status == StatusStopped {
			e := audit.Entry{ActorKind: audit.ActorSystem, Action: "trigger.run_stop", TargetType: "trigger_run", TargetID: run.ID.String(),
				TeamID: run.TeamID, Metadata: map[string]any{"triggerId": run.TriggerID.UUID.String(), "triggerName": run.TriggerName,
					"agentId": run.AgentID.String(), "runReason": reason, "costUsd": done.CostUsd}}
			if run.ThreadID != nil {
				e.Metadata["threadId"] = *run.ThreadID
			}
			if err := audit.Record(ctx, q, e); err != nil {
				return err
			}
		}
		if trig == nil || (status == StatusStopped && reason != ReasonMaxRunTime) {
			return nil
		}
		p := dbgen.TriggerOutcomeParams{ID: trig.ID}
		if failure(status, reason) {
			p.ConsecutiveFailures = trig.ConsecutiveFailures + 1
			until := now.Add(min(time.Minute<<min(p.ConsecutiveFailures-1, 10), time.Hour))
			p.BackoffUntil, p.Pause = &until, int(p.ConsecutiveFailures) >= s.cfg.PauseAfterFailures
		}
		updated, err := q.TriggerOutcome(ctx, p)
		if err != nil {
			return err
		}
		if updated.Paused && !trig.Paused {
			e := audit.Entry{ActorKind: audit.ActorSystem, Action: "trigger.auto_pause", TargetType: "trigger", TargetID: trig.ID.String(),
				TeamID: run.TeamID, Metadata: map[string]any{"name": trig.Name, "agentId": trig.AgentID.String(),
					"failures": updated.ConsecutiveFailures, "lastRunId": run.ID.String()}}
			if err := audit.Record(ctx, q, e); err != nil {
				return err
			}
			s.log.Info("triggers: paused after failures", "trigger", trig.ID, "failures", updated.ConsecutiveFailures)
		}
		return nil
	})
	if err != nil {
		s.log.Warn("triggers: finish a run", "run", run.ID, "err", err)
	}
}
