package triggers

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// lockKey names the lock of the replica that runs the trigger loop's pass.
const lockKey = "pdp.trigger-loop"

// Loop runs Tick every TRIGGERS_INTERVAL until ctx ends.
func (s *Service) Loop(ctx context.Context, onPass func() func(error)) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		done := func(error) {}
		if onPass != nil {
			done = onPass()
		}
		err := s.Tick(ctx)
		if ctx.Err() == nil {
			done(err)
		}
	}
}

// Tick is one pass of the trigger loop: fire due schedules, deliver runs
// waiting for delivery, check runs in progress (their end, their limits),
// and send due callbacks. One replica at a time (a database lock); the
// others skip the pass.
func (s *Service) Tick(ctx context.Context) error {
	if s.locker != nil {
		release, ok, err := s.locker.TryLock(ctx, lockKey)
		if err != nil || !ok {
			return err
		}
		defer release()
	}
	// The runs fired are delivered with the others waiting for delivery.
	if _, err := s.fireDue(ctx); err != nil {
		return err
	}
	if err := s.deliverPending(ctx); err != nil {
		return err
	}
	if err := s.supervise(ctx); err != nil {
		return err
	}
	return s.sendCallbacks(ctx)
}

// missedAfter is how late a schedule's fire may be and still run as on
// time; later, it was missed (the platform wasn't running).
func (s *Service) missedAfter() time.Duration { return max(2*s.cfg.Interval, time.Minute) }

// fireDue fires every schedule due now, in one transaction that advances
// them (locked: SKIP LOCKED leaves those another replica holds), and
// returns the runs to deliver.
func (s *Service) fireDue(ctx context.Context) ([]dbgen.TriggerRun, error) {
	now := s.now()
	var out []dbgen.TriggerRun
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		rows, err := q.DueSchedules(ctx, now)
		if err != nil {
			return err
		}
		for _, row := range rows {
			t := row.AgentTrigger
			sched, err := parseSchedule(*t.Cron, *t.Timezone)
			if err != nil { // stored before a rule changed: it stops
				s.log.Warn("triggers: a schedule can't be read; it stops firing", "trigger", t.ID)
				if err := q.AdvanceSchedule(ctx, dbgen.AdvanceScheduleParams{ID: t.ID}); err != nil {
					return err
				}
				continue
			}
			due := *t.NextFireAt
			next := sched.next(now)
			advance := dbgen.AdvanceScheduleParams{ID: t.ID, NextFireAt: &next}
			l, err := s.limitsOf(ctx, q, t.AgentID)
			if err != nil {
				return err
			}
			f := fire{trig: t, teamID: row.TeamID, firedBy: FiredBySchedule}
			// An archived team's schedules, and a paused agent's, move on without
			// runs. A one-time reminder has no next time: its run is recorded as
			// skipped, and it is removed.
			if row.TeamStatus != teams.StatusActive || l.paused {
				if !t.Once {
					if err := q.AdvanceSchedule(ctx, advance); err != nil {
						return err
					}
					continue
				}
				f.skip = ReasonAgentPaused
				if row.TeamStatus != teams.StatusActive {
					f.skip = ReasonTeamArchived
				}
			}
			if now.Sub(due) > s.missedAfter() {
				last := sched.lastBefore(due, now)
				due = last
				if *t.MissedPolicy != MissedRunOnce {
					f.skip = ReasonMissed
				}
				s.log.Info("triggers: a schedule missed its time", "trigger", t.ID, "policy", *t.MissedPolicy)
			}
			at := due
			f.at, f.fireID = &at, scheduleFireID(t.ID, due)
			f.message = messageFor(t, fireVars{TriggerName: t.Name, TriggerKind: t.Kind, FireID: f.fireID, FireTime: due})
			run, replayed, err := s.record(ctx, q, f, now)
			if err != nil {
				return err
			}
			if t.Once {
				// Fired: a one-time reminder has no next time. It removes itself
				// when its run ends (finish), or now when nothing runs.
				if replayed || run.Status != StatusPending {
					if err := removeFired(ctx, q, t, row.TeamID, run.ID); err != nil {
						return err
					}
					continue
				}
				if err := q.AdvanceSchedule(ctx, dbgen.AdvanceScheduleParams{ID: t.ID, FiredAt: &now}); err != nil {
					return err
				}
				out = append(out, run)
				continue
			}
			advance.FiredAt = &now
			if err := q.AdvanceSchedule(ctx, advance); err != nil {
				return err
			}
			if !replayed && run.Status == StatusPending {
				out = append(out, run)
			}
		}
		return nil
	})
	return out, err
}

// deliverPending delivers runs whose delivery is due: never tried (the
// replica that fired them stopped), or to be tried again.
func (s *Service) deliverPending(ctx context.Context) error {
	ids, err := s.q.PendingRunsDue(ctx, s.now())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dctx, cancel := context.WithTimeout(ctx, deliveryLease)
		s.deliver(dctx, id)
		cancel()
	}
	return nil
}

// supervise checks every run in progress: finished when its thread's reply
// ended (the runtime's report); stopped past its agent's maximum run time or
// once its trigger's or agent's spending today exceeds a budget; its cost so
// far updated otherwise.
func (s *Service) supervise(ctx context.Context) error {
	rows, err := s.q.RunningRuns(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := s.now()
		run := row.TriggerRun
		started := *run.StartedAt
		switch {
		case !row.ThreadBusy.Valid:
			s.finish(ctx, run, StatusFailed, ReasonThreadDeleted, now)
			continue
		case !row.ThreadBusy.Bool && row.ThreadStatusAt != nil && !row.ThreadStatusAt.Before(started):
			if row.ThreadLastError != nil {
				s.finish(ctx, run, StatusFailed, ReasonReplyError, now)
			} else {
				s.finish(ctx, run, StatusSucceeded, "", now)
			}
			continue
		}
		maxRun := s.cfg.MaxRunTime
		if row.MaxRunSeconds != nil {
			maxRun = min(maxRun, time.Duration(*row.MaxRunSeconds)*time.Second)
		}
		if now.Sub(started) > maxRun {
			// A run waiting for a person's approval of a tool call (docs/approvals.md) isn't working: its wait is
			// bounded by the approval's own expiry, which denies the call, and the run goes on.
			waiting := false
			if run.ThreadID != nil {
				if waiting, err = s.q.ThreadHasPendingToolApproval(ctx, dbgen.ThreadHasPendingToolApprovalParams{AgentID: run.AgentID, ThreadID: *run.ThreadID}); err != nil {
					return err
				}
			}
			if !waiting {
				s.stop(ctx, run, ReasonMaxRunTime, now)
				continue
			}
		}
		u, err := s.q.AgentUsageBetween(ctx, dbgen.AgentUsageBetweenParams{AgentID: run.AgentID, Since: started, Until: now.Add(time.Second)})
		if err != nil {
			return err
		}
		if err := s.q.UpdateRunCost(ctx, dbgen.UpdateRunCostParams{ID: run.ID, CostUsd: u.CostUsd, Requests: u.Requests}); err != nil {
			return err
		}
		over, err := s.overBudget(ctx, row, now)
		if err != nil {
			return err
		}
		if over {
			s.stop(ctx, run, ReasonBudget, now)
		}
	}
	return nil
}

// overBudget reports whether the run's trigger or agent has spent more than
// its budget today (the run's cost so far included).
func (s *Service) overBudget(ctx context.Context, row dbgen.RunningRunsRow, now time.Time) (bool, error) {
	since := dayStart(now)
	run := row.TriggerRun
	if row.TriggerDailyBudgetUsd.Valid {
		spent, err := s.q.TriggerCostSince(ctx, dbgen.TriggerCostSinceParams{TriggerID: run.TriggerID, Since: since})
		if err != nil || spent > row.TriggerDailyBudgetUsd.Float64 {
			return err == nil, err
		}
	}
	if row.AgentDailyBudgetUsd.Valid {
		spent, err := s.q.AgentRunCostSince(ctx, dbgen.AgentRunCostSinceParams{AgentID: run.AgentID, Since: since})
		if err != nil || spent > row.AgentDailyBudgetUsd.Float64 {
			return err == nil, err
		}
	}
	return false, nil
}

// stop aborts a run's thread and records it as stopped.
func (s *Service) stop(ctx context.Context, run dbgen.TriggerRun, reason string, now time.Time) {
	if run.ThreadID != nil {
		sctx, cancel := context.WithTimeout(ctx, time.Minute)
		err := s.runner.StopThread(sctx, run.AgentID, *run.ThreadID)
		cancel()
		if err != nil {
			// Recorded as stopped anyway: the limit was reached. The reply may
			// go on until the agent hibernates.
			s.log.Warn("triggers: stop a run's thread", "run", run.ID, "err", err)
		}
	}
	s.log.Info("triggers: run stopped", "run", run.ID, "reason", reason)
	s.finish(ctx, run, StatusStopped, reason, now)
}

// errNoCallback: the run's trigger, or its callback, is gone.
var errNoCallback = errors.New("no callback")
