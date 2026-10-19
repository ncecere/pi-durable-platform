package httpapi

import (
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ncecere/pi-durable-platform/internal/httpx"
	"github.com/ncecere/pi-durable-platform/internal/metrics"
	"github.com/ncecere/pi-durable-platform/internal/tracing"
)

// observation is one request seen by the metrics and the tracing: the same
// choke point (chain for the API, observeHandler for the gateway) counts it
// and spans it, by the mux's route pattern, never by path.
type observation struct {
	m        *metrics.Metrics
	listener string
	span     trace.Span
	start    time.Time
}

// observe starts observing r: a server span, and one more request in flight.
// The span is a new root that the platform's sampler decides: the trace
// context the caller sent is only linked, never obeyed (tracing.StartRequest).
// The returned request carries the span's context.
func observe(m *metrics.Metrics, listener string, r *http.Request) (*http.Request, *observation) {
	// Named by the method until the mux has chosen the route.
	ctx, span := tracing.StartRequest(r.Context(), r.Header, r.Method,
		attribute.String("http.request.method", metrics.Method(r.Method)),
		attribute.String("pdp.listener", listener))
	m.InFlight(listener, 1)
	return r.WithContext(ctx), &observation{m: m, listener: listener, span: span, start: time.Now()}
}

// end finishes the observation. r must be the request that was routed (its
// Pattern is set by the mux), status what was answered.
func (o *observation) end(r *http.Request, status int) {
	o.m.InFlight(o.listener, -1)
	o.m.HTTPRequest(o.listener, r.Method, r.Pattern, status, time.Since(o.start))
	route := metrics.RoutePattern(r.Pattern)
	o.span.SetName(metrics.Method(r.Method) + " " + route)
	o.span.SetAttributes(attribute.String("http.route", route))
	tracing.Status(o.span, status, false)
	o.span.End()
}

// observeHandler observes every request of next (the gateway, which has no
// middleware chain of its own).
func observeHandler(m *metrics.Metrics, listener string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, obs := observe(m, listener, r)
		sw := httpx.NewStatusWriter(w)
		defer func() {
			status := sw.Status()
			if status == 0 {
				status = http.StatusOK
			}
			if p := recover(); p != nil {
				obs.end(r, http.StatusInternalServerError)
				panic(p)
			}
			obs.end(r, status)
		}()
		next.ServeHTTP(sw, r)
	})
}
