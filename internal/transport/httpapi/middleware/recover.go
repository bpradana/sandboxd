package middleware

import (
	"fmt"
	"net/http"
	"runtime/debug"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
)

func NewRecoverMiddleware(logger *logging.Logger) fiber.Handler {
	return func(c fiber.Ctx) (err error) {
		defer func() {
			if r := recover(); r != nil {
				span := trace.SpanFromContext(c.Context())
				tracing.RecordError(span, panicError(r))
				logger.Ctx(c.Context()).Error(
					"http panic recovered",
					zap.Any("panic", r),
					zap.ByteString("stacktrace", debug.Stack()),
				)
				err = appctx.NewHTTPError(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)).WithErrorCode(appctx.CodeInternal)
			}
		}()
		return c.Next()
	}
}

func panicError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", value)
}
