// Package tracing defines the span naming and attribute conventions applied across the service.
package tracing

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// SpanStart instantiates a named span using the shared instrumentation prefix.
// Example: SpanStart(ctx, "service.sandbox.Create", ...)
func SpanStart(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "operation"
	}
	if rid := logging.RequestIDFromContext(ctx); rid != "" {
		opts = append(opts, trace.WithAttributes(attribute.String("request_id", rid)))
	}
	return otel.Tracer(strings.Split(name, ".")[0]).Start(ctx, name, opts...)
}

// SpanStartDetached starts a new root span for background work (reaper,
// teardown) that must outlive the request that triggered it, linking back to
// the current span when there is one.
func SpanStartDetached(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if parent := trace.SpanContextFromContext(ctx); parent.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: parent}))
	}
	opts = append(opts, trace.WithNewRoot())
	return SpanStart(context.WithoutCancel(ctx), name, opts...)
}
