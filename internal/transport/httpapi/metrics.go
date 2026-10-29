package httpapi

import (
	"strings"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/adaptor"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/metrics"
)

// MetricsPath is unauthenticated, like health checks, for cluster scrapers.
const MetricsPath = "/metrics"

type metricsParams struct {
	fx.In

	Server    *fiber.App
	Telemetry config.TelemetryConfig
	Metrics   *metrics.Registry `optional:"true"`
}

func registerMetricsRoute(params metricsParams) {
	if !params.Telemetry.MetricsEnabled || params.Metrics == nil {
		return
	}

	params.Server.Get(MetricsPath, adaptor.HTTPHandler(params.Metrics.Handler()))
}

func newMetricsMiddleware(registry *metrics.Registry) fiber.Handler {
	return func(c fiber.Ctx) error {
		// Scrapes do not measure themselves. This middleware never inspects
		// request bodies.
		if c.Path() == MetricsPath {
			return c.Next()
		}

		started := time.Now()
		err := c.Next()
		status := c.Response().StatusCode()
		if err != nil {
			status = appctx.NewErrorResponseBuilder(err).Status()
		}

		registry.ObserveHTTP(strings.Clone(c.Method()), metricsRoute(c), status, time.Since(started))

		return err
	}
}

func metricsRoute(c fiber.Ctx) string {
	route := c.Route()
	if !c.Matched() || route == nil || route.Path == "" {
		return "unmatched"
	}

	return strings.Clone(route.Path)
}
