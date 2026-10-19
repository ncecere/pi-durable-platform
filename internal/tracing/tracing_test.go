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

// installForTest installs the platform's provider (its sampler included) with
// an in-memory exporter.
func installForTest(t *testing.T, ratio float64) *tracetest.InMemoryExporter {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	shutdown, err := install(sdktrace.NewSimpleSpanProcessor(exp), Config{ServiceName: "pdp-under-test", SampleRatio: ratio})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()); otel.SetTracerProvider(prev) })
	return exp
}

const sampledByCaller = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// A caller's sampled flag does not defeat the sampling ratio: with ratio 0 the
// request is not recorded, and what the platform sends upstream says so.
func TestStartRequestIgnoresTheCallersSampledFlag(t *testing.T) {
	exp := installForTest(t, 0)
	in := http.Header{"Traceparent": {sampledByCaller}}
	ctx, span := StartRequest(context.Background(), in, "GET /v1/me")
	if span.SpanContext().IsSampled() || span.IsRecording() {
		t.Fatal("a caller's sampled flag forced sampling")
	}
	// The platform's own work below the request follows the request's decision.
	cctx, child := Start(ctx, "agent.wake", trace.SpanKindInternal)
	out := http.Header{}
	Inject(cctx, out)
	child.End()
	span.End()
	if got := out.Get("Traceparent"); !strings.HasSuffix(got, "-00") || strings.Contains(got, "4bf92f3577b34da6a3ce929d0e0e4736") {
		t.Errorf("upstream traceparent %q: want a trace of the platform's own, unsampled", got)
	}
	if n := len(exp.GetSpans()); n != 0 {
		t.Errorf("%d spans exported at ratio 0", n)
	}
}

// With sampling on, a request is a new root that links the caller's trace;
// spans below it follow it, and upstreams get the platform's trace context.
func TestStartRequestStartsARootAndLinksTheCaller(t *testing.T) {
	exp := installForTest(t, 1)
	ctx, span := StartRequest(context.Background(), http.Header{"Traceparent": {sampledByCaller}}, "POST /v1/x")
	cctx, child := Start(ctx, "agent.runtime_request", trace.SpanKindClient)
	out := http.Header{}
	Inject(cctx, out)
	child.End()
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans %v", spans)
	}
	server, down := spans[1], spans[0]
	if server.Parent.IsValid() || server.SpanContext.TraceID().String() == "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("the request span is not a root of its own: parent %v trace %s", server.Parent, server.SpanContext.TraceID())
	}
	if len(server.Links) != 1 || server.Links[0].SpanContext.SpanID().String() != "00f067aa0ba902b7" {
		t.Errorf("links %v", server.Links)
	}
	if down.Parent.SpanID() != server.SpanContext.SpanID() || down.SpanContext.TraceID() != server.SpanContext.TraceID() {
		t.Error("the span below the request does not follow it")
	}
	if got, want := out.Get("Traceparent"), "00-"+down.SpanContext.TraceID().String()+"-"+down.SpanContext.SpanID().String()+"-01"; got != want {
		t.Errorf("upstream traceparent %q, want %q", got, want)
	}
}

// Junk or no traceparent is a plain root, with no link.
func TestStartRequestWithoutAValidTraceparent(t *testing.T) {
	exp := installForTest(t, 1)
	for _, tp := range []string{"", "garbage", "00-00000000000000000000000000000000-0000000000000000-01"} {
		_, span := StartRequest(context.Background(), http.Header{"Traceparent": {tp}}, "GET /x")
		span.End()
	}
	for _, s := range exp.GetSpans() {
		if len(s.Links) != 0 || s.Parent.IsValid() {
			t.Errorf("span %+v", s)
		}
	}
	if n := len(exp.GetSpans()); n != 3 {
		t.Errorf("%d spans", n)
	}
}
