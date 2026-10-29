package logging

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newObservedLogger() (*Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return New(zap.New(core)), logs
}

func fieldMap(t *testing.T, logs *observer.ObservedLogs) map[string]string {
	t.Helper()
	entries := logs.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(entries))
	}
	out := make(map[string]string)
	for _, f := range entries[0].Context {
		out[f.Key] = f.String
	}
	return out
}

func TestCtxWithoutSpanOrRequestID(t *testing.T) {
	logger, logs := newObservedLogger()

	logger.Ctx(context.Background()).Info("hello")

	fields := fieldMap(t, logs)
	for _, key := range []string{"trace_id", "span_id", "dd.trace_id", "dd.span_id", "request_id"} {
		if _, ok := fields[key]; ok {
			t.Errorf("unexpected field %q", key)
		}
	}
}

func TestCtxWithSpanContext(t *testing.T) {
	logger, logs := newObservedLogger()

	traceID, _ := trace.TraceIDFromHex("463ac35c9f6413ad48485a3953bb6124")
	spanID, _ := trace.SpanIDFromHex("0020000000000001")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.Ctx(WithRequestID(ctx, "req-123")).Info("hello")

	fields := fieldMap(t, logs)
	expected := map[string]string{
		"trace_id":    "463ac35c9f6413ad48485a3953bb6124",
		"span_id":     "0020000000000001",
		"dd.trace_id": "5208512171318403364", // 0x48485a3953bb6124
		"dd.span_id":  "9007199254740993",    // 0x0020000000000001
		"request_id":  "req-123",
	}
	for key, want := range expected {
		if got := fields[key]; got != want {
			t.Errorf("field %q = %q, want %q", key, got, want)
		}
	}
}

func TestCtxWithIdentityFields(t *testing.T) {
	logger, logs := newObservedLogger()

	ctx := WithUserAgent(context.Background(), "sandboxd-test/1.0")

	logger.Ctx(ctx).Info("hello")

	fields := fieldMap(t, logs)
	expected := map[string]string{
		"useragent": "sandboxd-test/1.0",
	}
	for key, want := range expected {
		if got := fields[key]; got != want {
			t.Errorf("field %q = %q, want %q", key, got, want)
		}
	}
}

func TestWithRequestIDRoundTrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "abc")
	if got := RequestIDFromContext(ctx); got != "abc" {
		t.Errorf("RequestIDFromContext = %q, want %q", got, "abc")
	}
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("RequestIDFromContext on empty ctx = %q, want empty", got)
	}
	if ctx := WithRequestID(context.Background(), ""); RequestIDFromContext(ctx) != "" {
		t.Error("empty id should not be stored")
	}
}

func TestErrorSetsSpanStatus(t *testing.T) {
	logger, _ := newObservedLogger()

	recorder := sdktrace.NewTracerProvider()
	tracer := recorder.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "op")

	logger.Ctx(ctx).Error("boom")
	span.End()

	roSpan, ok := span.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("span is not a ReadOnlySpan")
	}
	if got := roSpan.Status().Code; got != codes.Error {
		t.Errorf("span status = %v, want %v", got, codes.Error)
	}
	if got := roSpan.Status().Description; got != "boom" {
		t.Errorf("span status description = %q, want %q", got, "boom")
	}
}
