// Package safego runs background goroutines with panic recovery so a single
// failing worker cannot take the whole process down.
package safego

import (
	"context"
	"fmt"
	"runtime/debug"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
)

// Go runs fn in a new goroutine, recovering and logging any panic with its
// stack trace. Mirrors the request-scoped recover middleware so background work
// (e.g. detached push-notification senders) gets the same protection.
//
// NOTE: this cannot catch panics raised inside net/http's own goroutines,
// because those run outside any goroutine we start. It only guards app code.
func Go(ctx context.Context, logger *logging.Logger, fn func()) {
	go func() {
		_ = Run(ctx, logger, func() error { fn(); return nil })
	}()
}

// Run calls fn synchronously and returns its error, converting a panic into an
// error after logging it with its stack trace. Use per iteration inside
// long-lived loops (event consumers, socket read loops) so one bad message is
// logged and skipped instead of killing the loop or the process.
func Run(ctx context.Context, logger *logging.Logger, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = panicError(r)
			tracing.RecordError(trace.SpanFromContext(ctx), err)
			if logger != nil {
				logger.Ctx(ctx).Error(
					"recovered panic",
					zap.Any("panic", r),
					zap.ByteString("stacktrace", debug.Stack()),
				)
			}
		}
	}()
	return fn()
}

func panicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", value)
}
