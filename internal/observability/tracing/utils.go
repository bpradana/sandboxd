package tracing

import (
	"context"
	"errors"
	"net"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
)

// RecordError records a standard span error using semantic conventions.
func RecordError(span trace.Span, err error, attrs ...attribute.KeyValue) {
	if span == nil || err == nil {
		return
	}
	if !span.IsRecording() {
		return
	}

	combined := make([]attribute.KeyValue, 0, len(attrs)+3)
	combined = append(combined, semconv.ErrorType(err))
	combined = append(combined, attribute.String("error.code", errorCode(err)))
	// Keep the message as an explicit attribute. Some instrumentation
	// libraries assign the final span status after inner middleware returns,
	// replacing the status description that SetStatus sets below.
	combined = append(combined, attribute.String("error.message", err.Error()))
	combined = append(combined, attrs...)
	span.SetAttributes(combined...)

	span.RecordError(err, trace.WithStackTrace(true)) // OTLP exception event
	span.SetStatus(codes.Error, err.Error())          // bridge error.message + OTLP status desc
}

// errorCode pulls a stable, low-cardinality code from anywhere in the error
// chain: first any error that self-describes via ErrorCode() (duck-typed to
// avoid an import cycle with appctx), then a fallback classifier for common
// stdlib sentinels that can't carry a method.
func errorCode(err error) string {
	var c interface{ ErrorCode() string }
	if errors.As(err, &c) {
		if code := c.ErrorCode(); code != "" {
			return code
		}
	}

	switch {
	case errors.Is(err, context.Canceled):
		return ErrorCodeCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorCodeDeadlineExceeded
	}

	if _, ok := errors.AsType[net.Error](err); ok {
		return ErrorCodeNetwork
	}
	// Never empty: every recorded error must land in the error.code facet.
	return ErrorCodeUnclassified
}

// AddInfo records a low-cardinality info event with semconv-compatible event metadata.
func AddInfo(span trace.Span, eventName string, attrs ...attribute.KeyValue) {
	if span == nil || !span.IsRecording() {
		return
	}
	eventName = strings.TrimSpace(eventName)
	if eventName == "" {
		return
	}

	combined := make([]attribute.KeyValue, 0, len(attrs)+2)
	combined = append(combined,
		semconv.OTelEventName(eventName),
		String(EventLevelKey, "info"),
	)
	combined = append(combined, attrs...)
	span.AddEvent(eventName, trace.WithAttributes(combined...))
}

func Attr(keySuffix string) attribute.Key {
	normalized := strings.TrimSpace(strings.TrimPrefix(keySuffix, "app."))
	if normalized == "" {
		normalized = "unknown"
	}
	return attribute.Key("app." + normalized)
}

// String clones value: attribute strings are exported by the batch span
// processor seconds after the handler returns, while Fiber's zero-copy request
// strings are only valid until then (fasthttp reuses the request buffer).
func String(keySuffix, value string) attribute.KeyValue {
	return Attr(keySuffix).String(strings.Clone(value))
}

func Int(keySuffix string, value int) attribute.KeyValue {
	return Attr(keySuffix).Int(value)
}

func Int64(keySuffix string, value int64) attribute.KeyValue {
	return Attr(keySuffix).Int64(value)
}

func Bool(keySuffix string, value bool) attribute.KeyValue {
	return Attr(keySuffix).Bool(value)
}

func Float64(keySuffix string, value float64) attribute.KeyValue {
	return Attr(keySuffix).Float64(value)
}

func StringSlice(keySuffix string, value []string) attribute.KeyValue {
	return Attr(keySuffix).StringSlice(value)
}
