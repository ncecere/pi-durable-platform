package tracing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTracesURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://collector:4318":     "http://collector:4318/v1/traces",
		"http://collector:4318/":    "http://collector:4318/v1/traces",
		"https://otel.example.com/": "https://otel.example.com/v1/traces",
	} {
		if got := tracesURL(in); got != want {
			t.Errorf("tracesURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// Without an endpoint nothing is exported and the spans are no-ops.
func TestSetupOffWithoutEndpoint(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, span := Start(context.Background(), "x", trace.SpanKindInternal)
	if span.SpanContext().IsValid() {
		t.Error("a span was recorded without a collector")
	}
}

// With an endpoint, spans reach the collector's /v1/traces over HTTP when
// the exporter is flushed.
func TestSetupExportsToTheCollector(t *testing.T) {
	var mu sync.Mutex
	var paths, types []string
	var body []byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths, types, body = append(paths, r.URL.Path), append(types, r.Header.Get("Content-Type")), append(body, b...)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer collector.Close()
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	shutdown, err := Setup(context.Background(), Config{Endpoint: collector.URL, ServiceName: "pdp-under-test", SampleRatio: 1, Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	_, span := Start(context.Background(), "GET /v1/me", trace.SpanKindServer)
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 || paths[0] != "/v1/traces" || !strings.Contains(types[0], "protobuf") {
		t.Fatalf("collector saw %v %v", paths, types)
	}
	for _, want := range []string{"pdp-under-test", "GET /v1/me", "9.9.9"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the exported spans lack %q", want)
		}
	}
}

func TestPropagation(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	in := http.Header{"Traceparent": {"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}, "Baggage": {"user=alice"}}
	ctx, span := Start(Extract(context.Background(), in), "server", trace.SpanKindServer)
	out := http.Header{}
	Inject(ctx, out)
	span.End()

	if got := out.Get("Traceparent"); !strings.HasPrefix(got, "00-4bf92f3577b34da6a3ce929d0e0e4736-") || strings.Contains(got, "00f067aa0ba902b7") {
		t.Errorf("the outgoing traceparent %q is not a child of the incoming trace", got)
	}
	if out.Get("Baggage") != "" {
		t.Error("baggage was propagated")
	}
	if s := exp.GetSpans(); len(s) != 1 || s[0].Parent.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("spans %v", s)
	}
}

func TestStatusAndFail(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	for _, tc := range []struct {
		status int
		client bool
		want   codes.Code
	}{{200, false, codes.Unset}, {404, false, codes.Unset}, {404, true, codes.Error}, {503, false, codes.Error}} {
		_, s := Start(context.Background(), "x", trace.SpanKindServer)
		Status(s, tc.status, tc.client)
		s.End()
	}
	_, s := Start(context.Background(), "failed", trace.SpanKindClient)
	Fail(s, "upstream unreachable")
	s.End()
	stubs := exp.GetSpans()
	for i, want := range []codes.Code{codes.Unset, codes.Unset, codes.Error, codes.Error, codes.Error} {
		if stubs[i].Status.Code != want {
			t.Errorf("span %d status %v, want %v", i, stubs[i].Status.Code, want)
		}
	}
}
