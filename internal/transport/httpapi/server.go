package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	fiberotel "github.com/gofiber/contrib/v3/otel"
	fiber "github.com/gofiber/fiber/v3"
	fibercompress "github.com/gofiber/fiber/v3/middleware/compress"
	fibercors "github.com/gofiber/fiber/v3/middleware/cors"
	fiberrequestid "github.com/gofiber/fiber/v3/middleware/requestid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/observability/metrics"
	"github.com/bpradana/sandboxd/internal/transport/httpapi/middleware"
)

type ServerParams struct {
	fx.In

	Lifecycle      fx.Lifecycle
	Logger         *logging.Logger
	Config         config.HTTPConfig
	App            config.AppConfig
	Exec           config.ExecConfig
	Telemetry      config.TelemetryConfig
	TracerProvider trace.TracerProvider `optional:"true"`
	Metrics        *metrics.Registry    `optional:"true"`
}

func NewServer(params ServerParams) (*fiber.App, error) {
	app := fiber.New(fiber.Config{
		// Sandbox ids, paths, and command strings outlive the handler in spans
		// and background teardown, so own the bytes returned by Fiber getters.
		Immutable:       true,
		ErrorHandler:    newHTTPErrorHandler(params.Logger),
		ReadTimeout:     params.Config.ReadTimeout,
		WriteTimeout:    maxDuration(params.Config.WriteTimeout, params.Config.StreamWriteTimeout),
		StructValidator: newRequestValidator(),
		// JSON bodies are capped here; file uploads above it are streamed and
		// capped by SANDBOX_FILE_MAX_BYTES instead of being buffered in memory.
		BodyLimit:         params.Config.BodyLimit,
		StreamRequestBody: true,
	})

	app.Use(newCORSMiddleware(params.App))
	app.Use(fiberrequestid.New())
	app.Use(func(c fiber.Ctx) error {
		// fiberrequestid stores the id in locals only (PassLocalsToContext is
		// unset), so relay it into the request context for log correlation.
		c.SetContext(logging.WithRequestID(c.Context(), fiberrequestid.FromContext(c)))
		return c.Next()
	})
	app.Use(fibercompress.New(fibercompress.Config{
		Level: fibercompress.LevelBestSpeed,
		// Compression buffers the body, which would hold back Server-Sent
		// Events and file downloads until the command or stream finishes.
		Next: func(c fiber.Ctx) bool {
			return isStreamingPath(c.Path())
		},
	}))

	if params.Telemetry.Enabled && params.TracerProvider != nil {
		app.Use(fiberotel.New(
			fiberotel.WithTracerProvider(params.TracerProvider),
			fiberotel.WithCustomAttributes(func(c fiber.Ctx) []attribute.KeyValue {
				return []attribute.KeyValue{
					attribute.String("http.method", strings.Clone(c.Method())),
					attribute.String("request_id", strings.Clone(fiberrequestid.FromContext(c))),
				}
			}),
			// fiberotel's request-size metric re-wraps the streamed request
			// body via SetBodyStream, which breaks StreamRequestBody uploads.
			fiberotel.WithoutMetrics(true),
			fiberotel.WithNext(func(c fiber.Ctx) bool {
				return isTraceExcludedPath(c.Path())
			}),
		))
		app.Use(middleware.NewServerSpanErrorMiddleware())
	}
	// Observe the error returned by recovery so panics count as HTTP 500s.
	if params.Telemetry.MetricsEnabled && params.Metrics != nil {
		app.Use(newMetricsMiddleware(params.Metrics))
	}

	app.Use(middleware.NewRecoverMiddleware(params.Logger))
	app.Use(middleware.NewRequestLogger(params.Logger))

	address := fmt.Sprintf("%s:%d", params.Config.Host, params.Config.Port)

	params.Lifecycle.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go func() {
				if err := app.Listen(address, fiber.ListenConfig{DisableStartupMessage: true}); err != nil && !errors.Is(err, fiber.ErrGracefulTimeout) && !errors.Is(err, fiber.ErrNotRunning) {
					params.Logger.Error("http server terminated", zap.Error(err))
				}
			}()
			params.Logger.Info("http server started", zap.String("address", address))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			params.Logger.Info("shutting down http server")
			return app.ShutdownWithContext(ctx)
		},
	})

	return app, nil
}

func newCORSMiddleware(cfg config.AppConfig) fiber.Handler {
	if len(cfg.CORSOrigins) == 0 {
		return func(c fiber.Ctx) error {
			return c.Next()
		}
	}

	return fibercors.New(fibercors.Config{
		AllowOrigins: cfg.CORSOrigins,
		AllowMethods: []string{
			fiber.MethodGet,
			fiber.MethodPost,
			fiber.MethodPut,
			fiber.MethodDelete,
			fiber.MethodHead,
			fiber.MethodOptions,
		},
		AllowHeaders: []string{
			fiber.HeaderAuthorization,
			fiber.HeaderContentType,
			fiber.HeaderAccept,
		},
	})
}

func maxDuration(left, right time.Duration) time.Duration {
	if right > left {
		return right
	}
	return left
}

func isStreamingPath(path string) bool {
	return strings.HasSuffix(path, "/exec/stream") || strings.HasSuffix(path, "/files") || isTerminalPath(path)
}

// isTerminalPath matches the terminal WebSocket upgrade.
func isTerminalPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/sandboxes/") && strings.HasSuffix(path, "/terminal")
}

// isTraceExcludedPath skips HTTP spans for probes and, because the HTTP span
// records the full URL, for the terminal upgrade whose query carries a
// ticket. The terminal's service span is still recorded.
func isTraceExcludedPath(path string) bool {
	switch path {
	case "/healthz", MetricsPath:
		return true
	default:
		return isTerminalPath(path)
	}
}
