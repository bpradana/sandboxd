package middleware

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
)

// NewServerSpanErrorMiddleware records returned and handled 5xx errors on the
// inbound server span. The span is captured before downstream handlers can
// replace the Fiber context with a child span.
func NewServerSpanErrorMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		serverSpan := trace.SpanFromContext(c.Context())
		err := c.Next()
		spanErr := err
		if spanErr == nil {
			spanErr = appctx.ResponseErrorFromContext(c.Context())
		}
		if spanErr == nil {
			return nil
		}

		status := c.Response().StatusCode()
		if err != nil {
			status = appctx.NewErrorResponseBuilder(err).Status()
		}
		if status >= http.StatusInternalServerError {
			tracing.RecordError(serverSpan, spanErr)
		}
		return err
	}
}
