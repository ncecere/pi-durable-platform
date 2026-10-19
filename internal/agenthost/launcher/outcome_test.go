package launcher

import (
	"context"
	"testing"
)

func ensureStart(t *testing.T, s *AgentSandbox) string {
	t.Helper()
	ctx, out := WithOutcome(context.Background())
	if _, err := s.Ensure(ctx, testSpec()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return out.Start()
}

func TestOutcomeWarmPool(t *testing.T) {
	s, _ := newTestSandbox(t, "pdp-runtimes", &fakeRuntime{})
	if got := ensureStart(t, s); got != StartWarm {
		t.Errorf("first wake = %q, want %q (the claim got a pool sandbox)", got, StartWarm)
	}
	if got := ensureStart(t, s); got != StartRunning {
		t.Errorf("second wake = %q, want %q", got, StartRunning)
	}
}

func TestOutcomePerAgentSandbox(t *testing.T) {
	s, _ := newTestSandbox(t, "", nil)
	if got := ensureStart(t, s); got != StartCold {
		t.Errorf("first wake = %q, want %q", got, StartCold)
	}
	if got := ensureStart(t, s); got != StartRunning {
		t.Errorf("second wake = %q, want %q", got, StartRunning)
	}
}

func TestClaimStart(t *testing.T) {
	var c sandboxClaim
	c.Status.Sandbox.Name = "pool-abc12"
	for _, tc := range []struct {
		sandbox string
		waited  bool
		want    string
	}{
		{"pool-abc12", false, StartRunning},
		{"pool-abc12", true, StartWarm},
		{"agent-a1b2", true, StartPoolMiss}, // named after the claim: created on demand
		{"", true, StartPoolMiss},
	} {
		c.Status.Sandbox.Name = tc.sandbox
		if got := c.start("agent-a1b2", tc.waited); got != tc.want {
			t.Errorf("sandbox %q waited %v: %q, want %q", tc.sandbox, tc.waited, got, tc.want)
		}
	}
}

func TestOutcomeReportPrecedence(t *testing.T) {
	ctx, out := WithOutcome(context.Background())
	report(ctx, StartRunning)
	report(ctx, StartCold)
	report(ctx, StartRunning) // does not undo the cold start
	if out.Start() != StartCold {
		t.Fatalf("start = %q", out.Start())
	}
	report(ctx, StartWarm)
	if out.Start() != StartWarm {
		t.Fatalf("start = %q", out.Start())
	}
	// Without an Outcome in the context, reporting is a no-op.
	report(context.Background(), StartCold)
}
