package triggers

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ncecere/pi-durable-platform/internal/agents"
	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Settings are an agent's limits for its triggered runs. Nil fields are the
// platform's defaults (one run at once, the ceiling's run time, no budget).
type Settings struct {
	Paused            bool
	MaxConcurrentRuns *int32
	MaxRunSeconds     *int32
	DailyBudgetUSD    *float64
	Revision          int64
	UpdatedAt         *time.Time
	// Effective limits now, and the platform's ceilings.
	EffectiveConcurrentRuns int32
	EffectiveRunSeconds     int32
	CeilingConcurrentRuns   int32
	CeilingRunSeconds       int32
}

func (s *Service) settings(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID) (Settings, error) {
	l, err := s.limitsOf(ctx, q, agentID)
	if err != nil {
		return Settings{}, err
	}
	out := Settings{Revision: l.revision, EffectiveConcurrentRuns: int32(l.maxConcurrent), EffectiveRunSeconds: int32(l.maxRun / time.Second),
		CeilingConcurrentRuns: int32(s.cfg.MaxConcurrentRuns), CeilingRunSeconds: int32(s.cfg.MaxRunTime / time.Second)}
	row, err := q.GetTriggerSettings(ctx, agentID)
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return out, nil
	} else if err != nil {
		return out, err
	}
	out.Paused, out.MaxConcurrentRuns, out.MaxRunSeconds, out.UpdatedAt = row.Paused, row.MaxConcurrentRuns, row.MaxRunSeconds, &row.UpdatedAt
	if row.DailyBudgetUsd.Valid {
		b := row.DailyBudgetUsd.Float64
		out.DailyBudgetUSD = &b
	}
	return out, nil
}

// GetSettings returns the agent's trigger limits.
func (s *Service) GetSettings(ctx context.Context, a authz.Actor, agentID uuid.UUID) (Settings, error) {
	if _, _, err := s.readable(ctx, a, agentID); err != nil {
		return Settings{}, err
	}
	return s.settings(ctx, s.q, agentID)
}

// SetSettings replaces the agent's trigger limits (expectedRevision: the
// one the caller loaded; 1 for an agent that has never had them set).
// Resuming the agent's triggers starts its schedules from now.
func (s *Service) SetSettings(ctx context.Context, a authz.Actor, agentID uuid.UUID, in Settings, expectedRevision int64) (Settings, error) {
	if in.MaxConcurrentRuns != nil && (*in.MaxConcurrentRuns < 1 || int(*in.MaxConcurrentRuns) > s.cfg.MaxConcurrentRuns) {
		return Settings{}, errBadLimits
	}
	if in.MaxRunSeconds != nil && (*in.MaxRunSeconds < 60 || time.Duration(*in.MaxRunSeconds)*time.Second > s.cfg.MaxRunTime) {
		return Settings{}, errBadLimits
	}
	var budget pgtype.Float8
	if b := in.DailyBudgetUSD; b != nil {
		if *b < 0 || *b > 1e6 || math.IsNaN(*b) {
			return Settings{}, errBadBudget
		}
		budget = pgtype.Float8{Float64: *b, Valid: true}
	}
	now := s.now()
	var out Settings
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, _, err := manageable(ctx, q, a, agentID)
		if err != nil {
			return err
		}
		before, err := s.settings(ctx, q, agentID)
		if err != nil {
			return err
		}
		if before.Revision != expectedRevision {
			return apperr.Stale()
		}
		if _, err := q.UpsertTriggerSettings(ctx, dbgen.UpsertTriggerSettingsParams{AgentID: agentID, Paused: in.Paused,
			MaxConcurrentRuns: in.MaxConcurrentRuns, MaxRunSeconds: in.MaxRunSeconds, DailyBudgetUsd: budget, Now: now,
			ExpectedRevision: expectedRevision}); errors.Is(err, pgx.ErrNoRows) {
			return apperr.Stale()
		} else if err != nil {
			return err
		}
		if out, err = s.settings(ctx, q, agentID); err != nil {
			return err
		}
		e := a.Audit("trigger_settings.update", "agent", agentID.String())
		e.TeamID = ag.TeamID
		e.Before, e.After = settingsSnapshot(before), settingsSnapshot(out)
		return audit.Record(ctx, q, e)
	})
	return out, err
}

func settingsSnapshot(st Settings) map[string]any {
	m := map[string]any{"paused": st.Paused}
	if st.MaxConcurrentRuns != nil {
		m["maxConcurrentRuns"] = *st.MaxConcurrentRuns
	}
	if st.MaxRunSeconds != nil {
		m["maxRunSeconds"] = *st.MaxRunSeconds
	}
	if st.DailyBudgetUSD != nil {
		m["dailyBudgetUsd"] = *st.DailyBudgetUSD
	}
	return m
}

// RunView is a run with the console's path of its thread.
type RunView struct {
	Run  dbgen.TriggerRun
	Path *string
}

// RunPage narrows a run list: one trigger's, one status's, after a run
// (created at, ID) in the list's order (newest first), at most Limit.
type RunPage struct {
	TriggerID *uuid.UUID
	Status    *string
	AfterAt   *time.Time
	AfterID   *uuid.UUID
	Limit     int32
}

func (s *Service) runViews(ctx context.Context, rows []dbgen.TriggerRun) []RunView {
	slugs := map[uuid.UUID]string{}
	out := make([]RunView, len(rows))
	for i, r := range rows {
		out[i].Run = r
		if r.ThreadID == nil {
			continue
		}
		slug, ok := slugs[r.AgentID]
		if !ok {
			slug, _ = s.q.AgentTeamSlug(ctx, r.AgentID)
			slugs[r.AgentID] = slug
		}
		if slug != "" {
			p := RunPath(slug, r.AgentID, *r.ThreadID)
			out[i].Path = &p
		}
	}
	return out
}

// Runs lists an agent's runs, newest first.
func (s *Service) Runs(ctx context.Context, a authz.Actor, agentID uuid.UUID, page RunPage) ([]RunView, error) {
	if _, _, err := s.readable(ctx, a, agentID); err != nil {
		return nil, err
	}
	p := dbgen.ListAgentRunsParams{AgentID: agentID, Status: page.Status, AfterAt: page.AfterAt, RowLimit: page.Limit}
	if page.TriggerID != nil {
		p.TriggerID = uuid.NullUUID{UUID: *page.TriggerID, Valid: true}
	}
	if page.AfterID != nil {
		p.AfterID = uuid.NullUUID{UUID: *page.AfterID, Valid: true}
	}
	rows, err := s.q.ListAgentRuns(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.runViews(ctx, rows), nil
}

// Run returns one of an agent's runs.
func (s *Service) Run(ctx context.Context, a authz.Actor, agentID, runID uuid.UUID) (RunView, error) {
	if _, _, err := s.readable(ctx, a, agentID); err != nil {
		return RunView{}, err
	}
	r, err := s.q.GetRun(ctx, dbgen.GetRunParams{ID: runID, AgentID: agentID})
	if errors.Is(store.NotFound(err), store.ErrNotFound) {
		return RunView{}, errRunNotFound
	} else if err != nil {
		return RunView{}, err
	}
	return s.runViews(ctx, []dbgen.TriggerRun{r})[0], nil
}

// TeamRuns lists the runs of a team's agents the actor may read, newest
// first (platform staff: every agent's).
func (s *Service) TeamRuns(ctx context.Context, a authz.Actor, teamRef string, page RunPage) ([]RunView, error) {
	team, err := teams.Resolve(ctx, s.q, teamRef)
	if err != nil {
		return nil, err
	}
	ids, err := s.q.TeamAgentIDs(ctx, team.ID)
	if err != nil {
		return nil, err
	}
	var readable []uuid.UUID
	for _, id := range ids {
		ag, err := s.q.GetAgent(ctx, id)
		if err != nil {
			continue
		}
		sub, err := subjectOf(ctx, s.q, a, ag)
		if err != nil {
			return nil, err
		}
		if authz.CheckTriggerRead(sub) == nil {
			readable = append(readable, id)
		}
	}
	if len(readable) == 0 {
		// Not a member and not staff: the team isn't theirs to see.
		if role, err := roleOf(ctx, s.q, a, team.ID); err != nil {
			return nil, err
		} else if role == "" && !a.CanReadPlatform() {
			return nil, apperr.NotFound("team_not_found", "Team not found")
		}
		return []RunView{}, nil
	}
	p := dbgen.ListTeamRunsParams{TeamID: team.ID, AgentIds: readable, Status: page.Status, AfterAt: page.AfterAt, RowLimit: page.Limit}
	if page.AfterID != nil {
		p.AfterID = uuid.NullUUID{UUID: *page.AfterID, Valid: true}
	}
	rows, err := s.q.ListTeamRuns(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.runViews(ctx, rows), nil
}

func subjectOf(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent) (authz.AgentSubject, error) {
	return agents.Subject(ctx, q, a, ag)
}

// roleOf is the actor's role in a team ("" when not on it).
func roleOf(ctx context.Context, q *dbgen.Queries, a authz.Actor, teamID uuid.UUID) (string, error) {
	if a.Service != nil {
		if a.Service.TeamID == teamID {
			return a.Service.Role, nil
		}
		return "", nil
	}
	return teams.MemberRole(ctx, q, teamID, a.UserID)
}
