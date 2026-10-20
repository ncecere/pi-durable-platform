package agents

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ncecere/pi-durable-platform/internal/authz"
	"github.com/ncecere/pi-durable-platform/internal/store/dbgen"
)

// The app-level event stream (OAuth Connect, DESIGN.md): a trusted app
// follows the thread changes of everyone who connected it on one stream,
// authenticated as itself (its secret), without holding their tokens. Each
// change is offered to the people who may see it, by the rules of their own
// GET /v1/agents/events through the app's token (a member of the agent's
// team, no platform role, so no staff reads; service-managed agents never;
// another's private thread and reserved threads never), and carries their
// app-stable subjects.
//
// Who the app's people are is read again for every change: a grant revoked
// (by the person, an admin, the app, refresh token reuse or the group
// re-check), a connection whose refresh token expired, or a person suspended
// or denied stops their events at the next change. The agent is cached per
// stream until the next re-check (every StreamRecheck), when still() also
// says whether the app may still stream (its secret exists and it is still
// trusted); when it can't, the channel closes.

// AppThreadEvent is a thread change and the people (their subjects) it is
// for.
type AppThreadEvent struct {
	ThreadEvent
	Subjects []uuid.UUID
}

// SubscribeAppThreads returns the thread changes the people who connected
// clientID (for resource, the platform's APP_URL) may see, until ctx ends
// or still reports false at a re-check.
func (s *Service) SubscribeAppThreads(ctx context.Context, clientID, resource string, still func(context.Context) bool) <-chan AppThreadEvent {
	raw := make(chan ThreadEvent, 256)
	s.hub.mu.Lock()
	s.hub.subs[raw] = struct{}{}
	s.hub.mu.Unlock()
	s.Metrics.ThreadSubscribers(1)
	out := make(chan AppThreadEvent, 256)
	go func() {
		defer close(out)
		defer func() {
			s.hub.mu.Lock()
			delete(s.hub.subs, raw)
			s.hub.mu.Unlock()
			s.Metrics.ThreadSubscribers(-1)
		}()
		agentsSeen := map[uuid.UUID]*dbgen.Agent{} // nil: gone
		tick := time.NewTicker(s.streamRecheck())
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !still(ctx) {
					return
				}
				clear(agentsSeen)
			case ev := <-raw:
				ag, seen := agentsSeen[ev.AgentID]
				if !seen {
					if a, err := s.load(ctx, s.q, ev.AgentID); err == nil {
						ag = &a
					} else if ctx.Err() != nil {
						return
					}
					agentsSeen[ev.AgentID] = ag
				}
				if ag == nil {
					continue
				}
				subjects, err := s.appAudience(ctx, *ag, clientID, resource, ev.Thread.Thread)
				if err != nil {
					if ctx.Err() == nil {
						slog.WarnContext(ctx, "app event stream: who may see a change", "agent", ev.AgentID, "err", err)
					}
					continue
				}
				if len(subjects) == 0 {
					continue
				}
				select {
				case out <- AppThreadEvent{ThreadEvent: ev, Subjects: subjects}:
				default: // a slow reader misses events, never blocks others
				}
			}
		}
	}()
	return out
}

// appAudience is the subjects of the app's people who may see a change to
// thread t of agent ag now.
func (s *Service) appAudience(ctx context.Context, ag dbgen.Agent, clientID, resource string, t Thread) ([]uuid.UUID, error) {
	people, err := s.q.AppStreamPeople(ctx, dbgen.AppStreamPeopleParams{TeamID: ag.TeamID, ClientID: clientID, Resource: resource})
	if err != nil {
		return nil, err
	}
	var out []uuid.UUID
	for _, p := range people {
		// The person through the app: their team role, no platform role.
		a := authz.Actor{UserID: p.UserID, PlatformRole: authz.PlatformNone, OAuthGrantID: p.GrantID, OAuthClientName: p.ClientName}
		if authz.CheckAgentUse(authz.AgentSubject{Role: p.Role, ServiceManaged: ag.ServiceManaged}) != nil || !listable(a, t) {
			continue
		}
		out = append(out, p.Subject)
	}
	return out, nil
}
