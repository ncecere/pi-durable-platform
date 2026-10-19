package httpapi_test

import (
	"io"
	"net/http"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// A caller's traceparent with the sampled flag set must not make the public
// API or the gateway record a trace when the sampling ratio is 0
// (OTEL_TRACES_SAMPLER_ARG): the listeners decide for themselves.
func TestInboundTraceparentDoesNotForceSampling(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	// The sampler the platform installs for a ratio of 0 (tracing.install).
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(0)))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	app := newTestApp(t, nil)

	for _, target := range []string{app.URL + "/v1/me", app.URL + "/no/such/page", app.GatewayURL + "/model-proxy/x/chat/completions", app.GatewayURL + "/mcp-proxy/server/x"} {
		for _, tp := range []string{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", ""} {
			req, _ := http.NewRequest("POST", target, nil)
			if tp != "" {
				req.Header.Set("Traceparent", tp)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}
	if spans := exp.GetSpans(); len(spans) != 0 {
		t.Fatalf("%d spans recorded at ratio 0, the first %q (a caller forced sampling)", len(spans), spans[0].Name)
	}
}
