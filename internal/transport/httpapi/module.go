// Package httpapi builds and runs the Fiber HTTP server together with its error handling, health checks, and docs endpoints.
package httpapi

import (
	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/transport/httpapi/middleware"
)

var Module = fx.Options(
	fx.Provide(
		NewServer,
	),
	middleware.Module,
	fx.Invoke(func(*fiber.App) {}),
	fx.Invoke(registerHealthRoutes),
	fx.Invoke(registerMetricsRoute),
	fx.Invoke(registerDocsRoutes),
	fx.Invoke(registerWebUIRoutes),
)
