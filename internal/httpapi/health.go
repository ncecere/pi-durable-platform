package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ncecere/pi-durable-platform/internal/httpapi/apitypes"
	"github.com/ncecere/pi-durable-platform/internal/httpx"
)

// Health serves liveness and readiness. Readiness fails while draining so
// Kubernetes stops routing traffic before the server shuts down.
type Health struct {
	checks   map[string]func(context.Context) error
	draining atomic.Bool
}

func NewHealth(checks map[string]func(context.Context) error) *Health {
	return &Health{checks: checks}
}

// SetDraining marks the process as shutting down.
func (h *Health) SetDraining() { h.draining.Store(true) }

// Live reports that the process is running. It never checks dependencies:
// a database outage must not cause Kubernetes to restart every pod.
func (h *Health) Live(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, apitypes.Health{Status: "ok", Checks: map[string]string{}})
}

// Ready reports whether this process should receive traffic.
func (h *Health) Ready(w http.ResponseWriter, r *http.Request) {
	out := apitypes.Health{Status: "ok", Checks: map[string]string{}}
	status := http.StatusOK
	if h.draining.Load() {
		out.Status, status = "draining", http.StatusServiceUnavailable
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	for name, check := range h.checks {
		if err := check(ctx); err != nil {
			out.Checks[name] = "unavailable"
			if out.Status == "ok" {
				out.Status = "unavailable"
			}
			status = http.StatusServiceUnavailable
			continue
		}
		out.Checks[name] = "ok"
	}
	httpx.JSON(w, status, out)
}
