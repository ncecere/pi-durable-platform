package agents

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/apperr"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/llm"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
	"github.com/ncecere/pi-durable-platform/internal/teams"
)

// Moving an agent to another team (or personal workspace).
//
// Only the platform's records change: the agent keeps its ID, its schema
// and role in the agents database (its conversations), its template version
// and its threads' records (creators, visibility, participants). From then
// on it is the target team's, under that team's rules:
//
//   - Who may move it: authz.CheckAgentMove (owners and admins of both
//     teams; a personal workspace's owner only; platform admins between
//     shared teams). Service-managed agents don't move: they hold an app's
//     users' conversations, which no person of either team may read.
//   - What it needs there: its template must be one the target team may
//     deploy (the catalog's, or the team's own; archived or not: it is not a
//     new deployment), its model one the team may use (or modelId switches to
//     one), its name free among the team's agents, and room under the team's
//     limits. Each missing piece is a blocker, reported by the dry run and
//     refused (409) by the move.
//   - Threads: shared threads become visible to everyone who may use the
//     agent in the target team; private ones stay their creator's (out of
//     reach while the creator can't use the agent there).
//   - What stops applying: the old team's service accounts (their keys no
//     longer reach it), its external key (dropped: a key names an agent
//     within one team, and in a shared team it would make the agent
//     service-managed), the MCP servers apps set on its threads (removed:
//     their headers are credentials the old team's callers gave, which the
//     new team's people must not get to use), and the old team's own models
//     and MCP servers, and platform ones published to the old team only
//     (the runtime takes its servers and model from the team it belongs to;
//     a model the target team may not use is a blocker, servers are dropped).
//   - Usage already recorded stays with the team it happened in
//     (model_usage.team_id); new usage is the target team's.
//   - The runtime is stopped, so it starts with the target team's
//     configuration; open event streams of people who lose access end at
//     their next access check.
//   - Audited in both teams' logs (agent.move).

var (
	errMoveSameTeam       = apperr.Conflict("move_same_team", "The agent is already in that team")
	errMoveTemplate       = apperr.Conflict("move_template_unavailable", "The agent's template isn't available to the target team: it is another team's and not in the catalog")
	errMoveModel          = apperr.Conflict("move_model_unavailable", "The agent's model isn't available to the target team: choose a model the team may use")
	errMoveServiceManaged = apperr.Conflict("agent_service_managed", "A service-managed agent holds an app's users' conversations and stays in the app's team")
)

// MovePlan is what moving an agent to another team changes.
type MovePlan struct {
	AgentID  uuid.UUID
	From, To dbgen.Team
	// Blockers are the refusals the move would meet (none: allowed).
	Blockers []*apperr.Error
	Threads  dbgen.AgentMoveThreadsRow
	Access   dbgen.AgentMoveAccessRow
	// ExternalKey is the key the move drops (nil: none).
	ExternalKey *string
	// ThreadMCPServers is how many servers apps set on its threads (removed).
	ThreadMCPServers int64
	// MCPServersDropped names the template version's servers that apply in
	// the current team and not in the target team.
	MCPServersDropped []string
	// Model is the model it would run on there (nil: the platform's default).
	Model *ModelRef

	modelID uuid.NullUUID // the agent's own model choice after the move
}

// Allowed reports whether the move would be done now.
func (p MovePlan) Allowed() bool { return len(p.Blockers) == 0 }

// PlanMove is the dry run of Move: authorized the same way, it returns what
// the move would change, and what would stop it.
func (s *Service) PlanMove(ctx context.Context, a authz.Actor, id uuid.UUID, targetRef string, modelID *uuid.UUID) (MovePlan, error) {
	ag, err := s.load(ctx, s.q, id)
	if err != nil {
		return MovePlan{}, err
	}
	from, err := s.q.GetTeamByID(ctx, ag.TeamID)
	if err != nil {
		return MovePlan{}, err
	}
	to, err := teams.Resolve(ctx, s.q, targetRef)
	if err != nil {
		return MovePlan{}, err
	}
	return s.planMove(ctx, s.q, a, ag, from, to, modelID)
}

// Move moves the agent, with its conversations, to another team (see the
// package comment above). modelID switches its model as part of the move.
func (s *Service) Move(ctx context.Context, a authz.Actor, id uuid.UUID, targetRef string, modelID *uuid.UUID, expectedRevision int64) (View, error) {
	var out View
	err := store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := q.LockAgent(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return errNotFound
		} else if err != nil {
			return err
		}
		from, err := q.GetTeamByID(ctx, ag.TeamID)
		if err != nil {
			return err
		}
		to, err := teams.Resolve(ctx, q, targetRef)
		if err != nil {
			return err
		}
		// Both teams are locked (in a fixed order), as creating an agent locks its team: limits and names hold.
		first, second := &from, &to
		if to.ID.String() < from.ID.String() {
			first, second = &to, &from
		}
		if *first, err = q.LockTeam(ctx, first.ID); err != nil {
			return err
		}
		if second.ID != first.ID {
			if *second, err = q.LockTeam(ctx, second.ID); err != nil {
				return err
			}
		}
		plan, err := s.planMove(ctx, q, a, ag, from, to, modelID)
		if err != nil {
			return err
		}
		if ag.Revision != expectedRevision {
			return apperr.Stale()
		}
		if !plan.Allowed() {
			return plan.Blockers[0]
		}
		moved, err := q.MoveAgent(ctx, dbgen.MoveAgentParams{ID: id, TeamID: to.ID, ModelID: plan.modelID})
		if apperr.IsUniqueViolation(err, "agents_team_name_key") {
			return errNameTaken
		} else if err != nil {
			return err
		}
		removed, err := q.DeleteAgentThreadMCPServers(ctx, id)
		if err != nil {
			return err
		}
		// One entry in each team's log, so both teams' owners see it.
		for _, teamID := range []uuid.UUID{from.ID, to.ID} {
			e := a.Audit("agent.move", "agent", id.String())
			e.TeamID = teamID
			e.Before, e.After = moveSnapshot(ag, from), moveSnapshot(moved, to)
			meta := map[string]any{}
			for k, v := range e.Metadata {
				meta[k] = v
			}
			meta["fromTeam"], meta["toTeam"] = from.Slug, to.Slug
			meta["sharedThreads"], meta["privateThreads"] = plan.Threads.Shared, plan.Threads.Private
			meta["threadMcpServersRemoved"] = removed
			if ag.ExternalKey != nil {
				meta["externalKeyDropped"] = *ag.ExternalKey
			}
			e.Metadata = meta
			if err := audit.Record(ctx, q, e); err != nil {
				return err
			}
		}
		out, err = s.view(ctx, q, moved)
		return err
	})
	if err == nil {
		// It starts with the target team's model and servers on the next request.
		// Hibernate waits for a start under way (which may have read the agent
		// before the commit) and stops it too; a later start reads the agent as
		// moved (agenthost.Manager.Hibernate). Not cut short by the caller
		// leaving: the runtime must not go on with the old team's configuration.
		if herr := s.host.Hibernate(context.WithoutCancel(ctx), id.String()); herr != nil {
			slog.Warn("stop a moved agent's runtime: it keeps the old team's configuration until it goes idle",
				"agent", id, "err", herr)
		}
		out.Running = false
	}
	return out, err
}

func moveSnapshot(ag dbgen.Agent, t dbgen.Team) map[string]any {
	m := snapshot(ag)
	m["teamId"], m["team"] = t.ID.String(), t.Slug
	return m
}

// planMove authorizes the move of ag from one team to another and works out
// what it changes. Refusals that depend on the target team's resources are
// blockers; who may move it is decided here (an error).
func (s *Service) planMove(ctx context.Context, q *dbgen.Queries, a authz.Actor, ag dbgen.Agent, from, to dbgen.Team, modelID *uuid.UUID) (MovePlan, error) {
	sub, err := subject(ctx, q, a, ag)
	if err != nil {
		return MovePlan{}, err
	}
	if err := authz.CheckAgentSee(sub); err != nil {
		return MovePlan{}, err
	}
	toRole, err := roleIn(ctx, q, a, to.ID)
	if err != nil {
		return MovePlan{}, err
	}
	if err := authz.CheckAgentMove(authz.MoveSide{Role: sub.Role, Personal: from.Kind == teams.KindPersonal},
		authz.MoveSide{Role: toRole, Personal: to.Kind == teams.KindPersonal}, a.Service != nil, a.IsPlatformAdmin(), a.CanReadPlatform()); err != nil {
		return MovePlan{}, err
	}
	if from.ID == to.ID {
		return MovePlan{}, errMoveSameTeam
	}
	if from.Status != teams.StatusActive || to.Status != teams.StatusActive {
		return MovePlan{}, errTeamArchived
	}
	p := MovePlan{AgentID: ag.ID, From: from, To: to, ExternalKey: ag.ExternalKey, modelID: ag.ModelID, MCPServersDropped: []string{}}
	block := func(e *apperr.Error) { p.Blockers = append(p.Blockers, e) }
	if ag.ServiceManaged {
		block(errMoveServiceManaged)
	}

	// Its template, version and model, as the target team may use them.
	tpl, err := q.GetAgentTemplate(ctx, ag.TemplateID)
	if err != nil {
		return MovePlan{}, err
	}
	if tpl.TeamID.Valid && tpl.TeamID.UUID != to.ID && tpl.Status != authz.TemplatePublished {
		block(errMoveTemplate)
	}
	if modelID != nil {
		p.modelID = uuid.NullUUID{UUID: *modelID, Valid: true}
	}
	tv, err := version(ctx, q, ag.TemplateID, ag.TemplateVersion)
	if err != nil {
		return MovePlan{}, err
	}
	if mid, _ := effectiveModel(dbgen.Agent{ModelID: p.modelID}, tv); mid != uuid.Nil {
		if err := llm.CheckUsable(ctx, q, mid, to.ID); errors.Is(err, llm.ErrUnavailable) {
			block(errMoveModel)
		} else if err != nil {
			return MovePlan{}, err
		}
		if _, err := q.GetModelSummary(ctx, mid); err == nil {
			there := ag
			there.TeamID, there.ModelID = to.ID, p.modelID
			if p.Model, err = s.modelRef(ctx, q, there); err != nil {
				return MovePlan{}, err
			}
		} else if !errors.Is(store.NotFound(err), store.ErrNotFound) {
			return MovePlan{}, err
		}
	}

	// Its name (it has no key there) and the target team's limits.
	if taken, err := q.AgentNameTaken(ctx, dbgen.AgentNameTakenParams{TeamID: to.ID, Name: ag.Name, ID: ag.ID}); err != nil {
		return MovePlan{}, err
	} else if taken {
		block(errNameTaken)
	}
	if limits, err := teams.LimitsOf(ctx, q, to); err != nil {
		return MovePlan{}, err
	} else if limits.MaxAgents != nil {
		if n, err := q.CountAgentsInTeam(ctx, to.ID); err != nil {
			return MovePlan{}, err
		} else if n >= int64(*limits.MaxAgents) {
			if e, ok := apperr.As(teams.LimitError("agents", *limits.MaxAgents)); ok {
				block(e)
			}
		}
	}
	if to.Kind != teams.KindPersonal {
		if n, err := q.CountTeamAgents(ctx, to.ID); err != nil {
			return MovePlan{}, err
		} else if n >= MaxPerTeam {
			block(errTooMany)
		}
	}

	// What changes for its threads and for who reaches it.
	if p.Threads, err = q.AgentMoveThreads(ctx, dbgen.AgentMoveThreadsParams{AgentID: ag.ID, TargetTeam: to.ID}); err != nil {
		return MovePlan{}, err
	}
	if p.Access, err = q.AgentMoveAccess(ctx, dbgen.AgentMoveAccessParams{SourceTeam: from.ID, TargetTeam: to.ID}); err != nil {
		return MovePlan{}, err
	}
	if p.ThreadMCPServers, err = q.CountAgentThreadMCPServers(ctx, ag.ID); err != nil {
		return MovePlan{}, err
	}
	servers, err := q.ListTemplateVersionMCP(ctx, dbgen.ListTemplateVersionMCPParams{TemplateID: ag.TemplateID, Version: ag.TemplateVersion})
	if err != nil {
		return MovePlan{}, err
	}
	for _, sv := range servers {
		here, err := q.MCPServerAvailable(ctx, dbgen.MCPServerAvailableParams{ServerID: sv.ServerID, TeamID: from.ID})
		if err != nil {
			return MovePlan{}, err
		}
		there, err := q.MCPServerAvailable(ctx, dbgen.MCPServerAvailableParams{ServerID: sv.ServerID, TeamID: to.ID})
		if err != nil {
			return MovePlan{}, err
		}
		if here && !there {
			p.MCPServersDropped = append(p.MCPServersDropped, sv.Name)
		}
	}
	return p, nil
}
