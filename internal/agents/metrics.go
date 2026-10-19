package agents

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AgentsPool is the pool on the agents database (for its metrics).
func (s *Service) AgentsPool() *pgxpool.Pool { return s.agentsPool }

// ObserveAgents keeps the pdp_agents metric (how many agents exist, enabled
// or not) and pdp_agents_running current every interval until ctx ends.
// Every replica reports the same numbers.
func (s *Service) ObserveAgents(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		done := s.Metrics.Loop("agent_count")
		// Which runtimes are running is the launcher's to say, and every replica asks (the reaper pass that also sets this
		// runs on one replica at a time; dashboards take the maximum).
		s.Metrics.SetAgentsRunning(len(s.Host().Launcher().Running()))
		rows, err := s.q.CountAgentsByEnabled(ctx)
		if err == nil {
			var enabled, disabled int64
			for _, r := range rows {
				if r.Enabled {
					enabled = r.Total
				} else {
					disabled = r.Total
				}
			}
			s.Metrics.SetAgents(enabled, disabled)
		}

		if ctx.Err() == nil {
			done(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
