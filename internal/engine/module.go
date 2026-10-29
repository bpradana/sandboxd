package engine

import (
	"context"

	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

type Params struct {
	fx.In

	Lifecycle fx.Lifecycle
	Logger    *logging.Logger
	Sandbox   config.SandboxConfig
	Exec      config.ExecConfig
	Network   config.NetworkConfig
}

// Module provides the Engine and ties it to the application lifecycle. The
// invoke forces construction before the HTTP server so the engine's OnStop runs
// after the server has stopped accepting requests.
var Module = fx.Options(
	fx.Provide(newWithLifecycle),
	fx.Invoke(func(Engine) {}),
)

func newWithLifecycle(params Params) Engine {
	eng := New(params.Logger, params.Sandbox, params.Exec, params.Network)
	params.Lifecycle.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			return eng.Start(ctx)
		},
		OnStop: func(ctx context.Context) error {
			return eng.Stop(ctx)
		},
	})
	return eng
}
