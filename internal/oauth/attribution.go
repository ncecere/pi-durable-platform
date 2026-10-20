package oauth

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// Usage per connected app. Model and MCP requests are made by agents, not
// by apps: an app's token only sends the message an agent then answers. So
// the platform records, for every message it relays, who sent it and
// through which connected app (agent_run_senders: the latest sender of each
// thread, a grant or none), and the model gateway attributes a request of an
// agent to a grant when the agent's work at that moment is unambiguously
// one grant's: the latest senders of its threads that are replying, were
// sent to in the last minute or finished a reply in the last minute are all
// the same grant. Anything else (no such thread, a sender not through an
// app among them, or two apps) is attributed to none. A tool call on a
// thread's own MCP server knows its thread and looks at that one only.
//
// This never attributes one person's work to another's app, but it can
// miss: two people replying at once on one agent (a shared team agent) are
// both unattributed, and on a shared thread a reply counts for whoever sent
// last. The precise answer needs the runtime to tag each model request with
// its thread (not built). Existing usage reports don't change: the grant is
// one more column, read only by the app-level usage and the platform's
// filter by app.

// RecordRunSender records the latest sender of a thread (grantID zero: not
// through an app), before the message is relayed. Best effort: a failure
// is logged, and the message still goes (its usage is then unattributed or
// attributed by the thread's previous sender, until the next message).
func RecordRunSender(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID, threadID string, grantID uuid.UUID, at time.Time) {
	err := q.RecordRunSender(context.WithoutCancel(ctx), dbgen.RecordRunSenderParams{AgentID: agentID, ThreadID: threadID,
		GrantID: uuid.NullUUID{UUID: grantID, Valid: grantID != uuid.Nil}, SentAt: at})
	if err != nil {
		slog.WarnContext(ctx, "record a thread's sender", "agent", agentID, "err", err)
	}
}

// Attribution is the connected app (grant) an agent's request is attributed
// to now, if one is unambiguous (threadID "": any of the agent's threads).
// Best effort: a failure attributes none.
func Attribution(ctx context.Context, q *dbgen.Queries, agentID uuid.UUID, threadID string) uuid.NullUUID {
	var thread *string
	if threadID != "" {
		thread = &threadID
	}
	rows, err := q.RunSenderGrants(ctx, dbgen.RunSenderGrantsParams{AgentID: agentID, ThreadID: thread})
	if err != nil {
		slog.WarnContext(ctx, "attribute an agent's request", "agent", agentID, "err", err)
		return uuid.NullUUID{}
	}
	if len(rows) != 1 {
		return uuid.NullUUID{}
	}
	return rows[0]
}
