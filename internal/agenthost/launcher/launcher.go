// Package launcher starts and stops agent runtimes. A runtime is one Node
// process (or Kubernetes pod) running one Pi Durable agent; the platform starts
// it on demand and stops it when idle. Its state lives in PostgreSQL, so a
// stopped agent costs nothing and can start anywhere.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Spec is everything needed to run one agent.
type Spec struct {
	AgentID      string
	Name         string
	Instructions string
	// Schema and DatabaseURL are the agent's own storage and database role.
	Schema      string
	DatabaseURL string
	// Token is the bearer token the runtime requires from the platform.
	Token string
	// Env holds model settings (MODEL_PROVIDER, MODEL_BASE_URL, ...).
	Env map[string]string
	// Disabled: the agent is turned off. The manager never starts it (a request
	// authorized while it was on can still be on its way); launchers ignore it.
	Disabled bool
}

// Endpoint is where a running agent answers.
type Endpoint struct {
	BaseURL string
	Token   string
}

// Launcher runs agent runtimes. Implementations are safe for concurrent use
// and start at most one runtime per agent in this process; the manager
// (agenthost) serialises starts across platform replicas with a database
// lock around Ensure, so a launcher need not.
type Launcher interface {
	// Ensure starts the agent if it is not running and returns its endpoint
	// once it answers its health check. It is idempotent: called again for an
	// agent that another replica just started, it returns that runtime.
	Ensure(ctx context.Context, spec Spec) (Endpoint, error)
	// Lookup returns the endpoint of the agent's runtime when it is running
	// and ready, and false when it is not, without starting anything. It is
	// the fast path of every request, so it must be as cheap as one read of
	// the launcher's own state (an API call for the Kubernetes backends).
	Lookup(ctx context.Context, spec Spec) (Endpoint, bool, error)
	// Stop hibernates the agent: the runtime shuts down gracefully; work in
	// flight stays in storage and resumes on the next start.
	Stop(ctx context.Context, agentID string) error
	// Remove stops the agent and deletes everything the launcher keeps for it
	// (the agent itself is being deleted).
	Remove(ctx context.Context, agentID string) error
	// Running lists the agents with a live runtime and their endpoints.
	Running() map[string]Endpoint
	// Kind names the backend ("local", "kubernetes").
	Kind() string
}

// ErrOwnedElsewhere means another runtime still owns the agent's storage.
var ErrOwnedElsewhere = errors.New("agent storage is owned by another runtime")

// waitHealthy polls GET {base}/healthz until it answers 200 or ctx ends.
func waitHealthy(ctx context.Context, base string, exited <-chan struct{}) error {
	client := &http.Client{Timeout: 2 * time.Second}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent runtime did not become healthy: %w", ctx.Err())
		case <-exited:
			return errors.New("agent runtime exited during start-up")
		case <-tick.C:
		}
	}
}
