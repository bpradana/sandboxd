package bootstrap

import (
	"context"
	"fmt"

	"go.uber.org/fx"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// stacktraceLevel is where a stack trace earns its place. zap's development
// preset attaches one to every warning, which buries expected conditions --
// an unauthenticated development API, a backup that will be retried -- under
// pages of frames that say only how the logger was wired. Errors still carry
// one, because there the frames are the diagnosis.
const stacktraceLevel = zapcore.ErrorLevel

func NewLogger(lc fx.Lifecycle, cfg config.AppConfig, build buildinfo.Info) (*logging.Logger, error) {
	var zapCfg zap.Config
	if cfg.Env == "development" {
		zapCfg = zap.NewDevelopmentConfig()
	} else {
		zapCfg = zap.NewProductionConfig()
	}

	if cfg.LogLevel != "" {
		level, err := zapcore.ParseLevel(cfg.LogLevel)
		if err != nil {
			return nil, fmt.Errorf("parse LOG_LEVEL %q: %w", cfg.LogLevel, err)
		}
		zapCfg.Level = zap.NewAtomicLevelAt(level)
	}

	logger, err := buildZapLogger(zapCfg)
	if err != nil {
		return nil, err
	}

	base := logger.With(
		zap.String("service", cfg.Name),
		zap.String("env", cfg.Env),
		zap.String("version", build.Version),
		zap.String("commit", build.Commit),
	)

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			return base.Sync()
		},
	})

	return logging.New(base), nil
}

func buildZapLogger(zapCfg zap.Config) (*zap.Logger, error) {
	return zapCfg.Build(zap.AddStacktrace(stacktraceLevel))
}
