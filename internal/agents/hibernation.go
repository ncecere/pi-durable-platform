package agents

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ncecere/pi-durable-platform/internal/agenthost"
	"github.com/ncecere/pi-durable-platform/internal/audit"
	"github.com/ncecere/pi-durable-platform/internal/store"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// auditBackgroundAbort records a ceiling abort (agenthost.Manager's
// BackgroundAborted): the reaper had the agent's runtime abort background work
// older than AGENTS_BACKGROUND_MAX before hibernating it (docs/hibernation.md).
// The entry is the system's, in the agent's team's log. An agent deleted in
// the meantime is not audited.
func (s *Service) auditBackgroundAbort(ctx context.Context, agentID string, abort agenthost.BackgroundAbort) error {
	id, err := uuid.Parse(agentID)
	if err != nil {
		return err
	}
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		ag, err := q.GetAgent(ctx, id)
		if errors.Is(store.NotFound(err), store.ErrNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		meta := map[string]any{"aborted": abort.Aborted, "settled": abort.Settled, "ceiling": abort.Ceiling.String()}
		if !abort.OldestStartedAt.IsZero() {
			meta["oldestStartedAt"] = abort.OldestStartedAt.UTC()
		}
		return audit.Record(ctx, q, audit.Entry{ActorKind: audit.ActorSystem, TeamID: ag.TeamID, Action: "agent.background_abort",
			TargetType: "agent", TargetID: agentID, Metadata: meta})
	})
}
