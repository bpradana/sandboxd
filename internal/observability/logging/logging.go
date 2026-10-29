// Package logging wraps zap with the two things otelzap used to provide here
// (the rest of otelzap was a no-op: no OTel LoggerProvider is registered, so
// its log bridge never emitted anything):
//   - span.SetStatus(codes.Error, msg) on Error-level logs with a recording span
//   - trace/span/request correlation fields on every Ctx(ctx) log line, which
//     otelzap never actually added to zap output
package logging

import (
	"context"
	"encoding/binary"
	"strconv"
	"strings"

	fiberrequestid "github.com/gofiber/fiber/v3/middleware/requestid"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// Logger embeds *zap.Logger so bare calls (bootstrap, shutdown paths) keep
// working; request-scoped logging goes through Ctx.
type Logger struct {
	*zap.Logger
	// skipCaller compensates for the extra Ctx(...).Level(...) call frame so
	// the caller annotation points at the real call site.
	skipCaller *zap.Logger
}

func New(base *zap.Logger) *Logger {
	return &Logger{
		Logger:     base,
		skipCaller: base.WithOptions(zap.AddCallerSkip(1)),
	}
}

// Ctx mirrors otelzap.Logger.Ctx so existing logger.Ctx(ctx).<Level>(...) call
// sites need no changes.
func (l *Logger) Ctx(ctx context.Context) Ctx {
	return Ctx{ctx: ctx, l: l}
}

type Ctx struct {
	ctx context.Context
	l   *Logger
}

func (c Ctx) Debug(msg string, fields ...zap.Field) {
	c.l.skipCaller.Debug(msg, c.withCorrelation(fields)...)
}

func (c Ctx) Info(msg string, fields ...zap.Field) {
	c.l.skipCaller.Info(msg, c.withCorrelation(fields)...)
}

func (c Ctx) Warn(msg string, fields ...zap.Field) {
	c.l.skipCaller.Warn(msg, c.withCorrelation(fields)...)
}

func (c Ctx) Error(msg string, fields ...zap.Field) {
	if c.ctx != nil {
		if span := trace.SpanFromContext(c.ctx); span.IsRecording() {
			span.SetStatus(codes.Error, msg)
		}
	}
	c.l.skipCaller.Error(msg, c.withCorrelation(fields)...)
}

func (c Ctx) withCorrelation(fields []zap.Field) []zap.Field {
	if c.ctx == nil {
		return fields
	}
	if rid := RequestIDFromContext(c.ctx); rid != "" {
		fields = append(fields, zap.String("request_id", rid))
	}
	if ua := UserAgentFromContext(c.ctx); ua != "" {
		fields = append(fields, zap.String("useragent", ua))
	}
	sc := trace.SpanContextFromContext(c.ctx)
	if !sc.IsValid() {
		return fields
	}
	traceID, spanID := sc.TraceID(), sc.SpanID()
	return append(fields,
		zap.String("trace_id", traceID.String()),
		zap.String("span_id", spanID.String()),
		// Datadog log-trace correlation attributes: decimal, low 64 bits of
		// the 128-bit OTel trace ID, matching dd-trace-go's own convention.
		zap.String("dd.trace_id", strconv.FormatUint(binary.BigEndian.Uint64(traceID[8:]), 10)),
		zap.String("dd.span_id", strconv.FormatUint(binary.BigEndian.Uint64(spanID[:]), 10)),
	)
}

type ctxKey string

const requestIDKey ctxKey = "log_request_id"

// WithRequestID relays a request id into ctx so downstream logger.Ctx(ctx)
// calls tag their lines with it. fiberrequestid.FromContext(ctx) alone only
// reads from a plain context.Context when fiber.Config.PassLocalsToContext is
// enabled, which this app doesn't set — so the HTTP middleware and WS handler
// call this explicitly instead.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil || id == "" {
		return ctx
	}
	// Fiber's requestid middleware returns a client-sent X-Request-ID verbatim
	// (zero-copy into the pooled request buffer); this ctx feeds spans and
	// detached goroutines that log after the request ends. Detach here.
	return context.WithValue(ctx, requestIDKey, strings.Clone(id))
}

func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return fiberrequestid.FromContext(ctx)
}

type userAgentCtxKey struct{}

// WithUserAgent relays the client's User-Agent so both span attributes
// (tracing delegates here) and log lines can carry it. It lives in logging
// rather than tracing because tracing imports logging.
func WithUserAgent(ctx context.Context, ua string) context.Context {
	if ctx == nil || ua == "" {
		return ctx
	}
	return context.WithValue(ctx, userAgentCtxKey{}, ua)
}

func UserAgentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ua, _ := ctx.Value(userAgentCtxKey{}).(string)
	return ua
}
