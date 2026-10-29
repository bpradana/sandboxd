package httpapi

import (
	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

func newHTTPErrorHandler(logger *logging.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		if err == nil {
			return nil
		}

		// The request logger middleware already logged this error with full
		// request fields (fiber runs the error handler only after the whole
		// middleware chain unwinds). Only the response-write failure below is
		// a distinct, not-yet-observed error.
		if writeErr := appctx.NewErrorResponseBuilder(err).Build(c); writeErr != nil {
			logger.Ctx(c.Context()).Error("failed to write error response", zap.Error(writeErr))
			return writeErr
		}
		return nil
	}
}
