// Package bootstrap constructs the process-wide dependencies -- logging and telemetry -- consumed by the fx application.
package bootstrap

import (
	"go.uber.org/fx"
)

var Module = fx.Options(
	fx.Provide(
		NewLogger,
		NewTracerProvider,
	),
)
