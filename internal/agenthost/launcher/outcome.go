package launcher

import (
	"context"
	"sync"
)

// How Ensure got the agent a runtime (the start label of the wake metrics).
const (
	// StartRunning: the runtime was already up; nothing was started.
	StartRunning = "running"
	// StartCold: a runtime was started (or resumed) for the agent, or its start was already under way.
	StartCold = "cold"
	// StartWarm: the agent claimed a standby runtime of the warm pool.
	StartWarm = "warm"
	// StartPoolMiss: the agent made a claim on the warm pool but it was empty,
	// so a runtime was created on demand (as slow as a cold start).
	StartPoolMiss = "pool_miss"
)

// Outcome records how one Ensure call got its runtime. A caller that wants
// to know (the manager, for its metrics and traces) puts one in the context
// with WithOutcome; launchers report through it and never need to know
// whether anyone is looking.
type Outcome struct {
	mu    sync.Mutex
	start string
}

type outcomeKey struct{}

// WithOutcome returns a context whose Ensure calls report into the Outcome.
func WithOutcome(ctx context.Context) (context.Context, *Outcome) {
	o := &Outcome{}
	return context.WithValue(ctx, outcomeKey{}, o), o
}

// Start is how the runtime was obtained (the Start* constants), or "" when
// the launcher reported nothing (an Ensure that failed early).
func (o *Outcome) Start() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.start
}

// report notes how Ensure got its runtime. The first report of a call that
// started something wins over StartRunning, and warm-pool results win over
// StartCold.
func report(ctx context.Context, start string) {
	o, _ := ctx.Value(outcomeKey{}).(*Outcome)
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.start == "" || o.start == StartRunning || (o.start == StartCold && start != StartRunning) {
		o.start = start
	}
}
