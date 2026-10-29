package bootstrap

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

func NewTracerProvider(
	lc fx.Lifecycle,
	cfg config.TelemetryConfig,
	app config.AppConfig,
	build buildinfo.Info,
	logger *logging.Logger,
) (trace.TracerProvider, error) {
	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)

	if !cfg.Enabled {
		tp := noop.NewTracerProvider()
		otel.SetTracerProvider(tp)
		otel.SetTextMapPropagator(propagator)
		logger.Info("telemetry disabled")
		return tp, nil
	}

	sampleRate := cfg.SampleRate
	if sampleRate <= 0 || sampleRate > 1 {
		sampleRate = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		exporter sdktrace.SpanExporter
		err      error
	)

	switch cfg.Protocol {
	case "http":
		clientOptions := []otlptracehttp.Option{
			otlptracehttp.WithEndpoint(cfg.Endpoint),
			otlptracehttp.WithHeaders(cfg.Headers),
		}
		if cfg.Insecure {
			clientOptions = append(clientOptions, otlptracehttp.WithInsecure())
		}
		exporter, err = otlptracehttp.New(ctx, clientOptions...)

	case "", "grpc":
		clientOptions := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(cfg.Endpoint),
			otlptracegrpc.WithHeaders(cfg.Headers),
		}
		if cfg.Insecure {
			clientOptions = append(clientOptions, otlptracegrpc.WithInsecure())
		}
		exporter, err = otlptracegrpc.New(ctx, clientOptions...)

	default:
		return nil, fmt.Errorf("unsupported telemetry protocol %q", cfg.Protocol)
	}

	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}

	res, err := telemetryResource(app, build)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
		sdktrace.WithSpanProcessor(sdktrace.NewBatchSpanProcessor(exporter)),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagator)

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return tp.Shutdown(ctx)
		},
	})

	logger.Info(
		"tracing enabled with otlp exporter",
		zap.String("protocol", cfg.Protocol),
		zap.String("endpoint", cfg.Endpoint),
		zap.Float64("sample_rate", sampleRate),
	)

	return tp, nil
}

func telemetryResource(app config.AppConfig, build buildinfo.Info) (*resource.Resource, error) {
	return resource.New(
		context.Background(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(app.Name),
			semconv.ServiceVersionKey.String(build.Version),
			semconv.DeploymentEnvironmentName(app.Env),
		),
	)
}
