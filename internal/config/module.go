// Package config loads, validates, and exposes the typed application configuration.
package config

import "go.uber.org/fx"

var Module = fx.Options(
	fx.Provide(Load),

	fx.Provide(func(cfg Config) AppConfig { return cfg.App }),
	fx.Provide(func(cfg Config) HTTPConfig { return cfg.HTTP }),
	fx.Provide(func(cfg Config) TelemetryConfig { return cfg.Telemetry }),
	fx.Provide(func(cfg Config) SandboxConfig { return cfg.Sandbox }),
	fx.Provide(func(cfg Config) ExecConfig { return cfg.Exec }),
	fx.Provide(func(cfg Config) NetworkConfig { return cfg.Network }),
)
