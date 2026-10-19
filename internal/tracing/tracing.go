// Package tracing is the platform's optional OpenTelemetry tracing.
//
// It is off unless OTEL_EXPORTER_OTLP_ENDPOINT is set (Setup then does
// nothing and the spans below are no-ops). On, spans go to an OTLP/HTTP
// collector. The platform starts a server span per request, and child spans
// for waking an agent, relaying to its runtime, and the upstream calls of the
// model and MCP proxies; the trace context (W3C `traceparent`, never
// baggage) goes to those upstreams and to the runtime.
//
// What a span may hold: route patterns, status codes, launcher modes,
// provider kinds, platform model names and MCP server slugs, agent IDs.
// Never message content, headers, URLs, keys or tokens, and never a person's
// ID or e-mail address; call sites choose their attributes with that rule and
// the tests check it.
package tracing

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/ncecere/pi-durable-platform"

// Config is what Setup needs (OTEL_* settings).
type Config struct {
	// Endpoint is the collector's base URL (OTEL_EXPORTER_OTLP_ENDPOINT,
	// OTLP over HTTP, for example http://collector:4318). Empty: tracing off.
	Endpoint string
	// ServiceName is the service.name resource (OTEL_SERVICE_NAME).
	ServiceName string
	// SampleRatio is the share of new traces kept (OTEL_TRACES_SAMPLER_ARG,
	// 0 to 1); a request that arrives with a sampling decision keeps it.
	SampleRatio float64
	// Version is the platform's version (service.version).
	Version string
}

// Setup starts exporting spans to cfg.Endpoint and returns the function that
// flushes and stops it. With no endpoint it does nothing.
func Setup(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error) {
	if cfg.Endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(tracesURL(cfg.Endpoint)))
	if err != nil {
		return nil, fmt.Errorf("OTEL_EXPORTER_OTLP_ENDPOINT: %w", err)
	}
	return install(sdktrace.NewBatchSpanProcessor(exp), cfg)
}

// tracesURL is the collector's traces endpoint for its base URL (the OTLP/HTTP
// convention: base + /v1/traces).
func tracesURL(base string) string {
	return strings.TrimRight(base, "/") + "/v1/traces"
}

// install sets the global tracer provider.
func install(proc sdktrace.SpanProcessor, cfg Config) (func(context.Context) error, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", cfg.ServiceName),
		attribute.String("service.version", cfg.Version),
	))
	if err != nil {
		return nil, err
	}
	var sampler sdktrace.Sampler = sdktrace.AlwaysSample()
	if cfg.SampleRatio >= 0 && cfg.SampleRatio < 1 {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRatio)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(proc),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sampler)),
	)
	otel.SetTracerProvider(tp)
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}, nil
}

// Start begins a span named name. The tracer is looked up at each call so a
// provider installed later (tests) is used.
func Start(ctx context.Context, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

var propagator = propagation.TraceContext{}

// Extract returns ctx with the trace context of an incoming request.
func Extract(ctx context.Context, h http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(h))
}

// Inject adds ctx's trace context (traceparent, tracestate) to an outgoing
// request's headers.
func Inject(ctx context.Context, h http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(h))
}

// Fail marks the span as failed with a fixed reason (never an error's text,
// which may hold URLs or upstream bodies).
func Fail(span trace.Span, reason string) {
	span.SetStatus(codes.Error, reason)
}

// Status marks the span failed when an HTTP status says so (5xx for a server
// span; 4xx too for a client span).
func Status(span trace.Span, status int, client bool) {
	span.SetAttributes(attribute.Int("http.response.status_code", status))
	if status >= 500 || (client && status >= 400) {
		span.SetStatus(codes.Error, "")
	}
}
