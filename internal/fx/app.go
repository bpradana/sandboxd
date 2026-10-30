// Package fx assembles the runtime application graph from the config, bootstrap, repository, engine, service, handler, route, and transport modules.
package fx

import (
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/bootstrap"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/handler"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/observability/metrics"
	"github.com/bpradana/sandboxd/internal/preview"
	"github.com/bpradana/sandboxd/internal/repository"
	"github.com/bpradana/sandboxd/internal/route"
	"github.com/bpradana/sandboxd/internal/service"
	"github.com/bpradana/sandboxd/internal/transport/httpapi"
)

// Module order matters for shutdown: fx stops hooks in reverse start order,
// and engine and service force their construction before the HTTP server.
// On SIGTERM the server stops accepting requests first, then the reaper stops,
// then the engine destroys every remaining sandbox.
var Module = fx.Options(
	config.Module,
	fx.Provide(func(app config.AppConfig) buildinfo.Info {
		return buildinfo.Read(app.Env)
	}),
	bootstrap.Module,
	metrics.Module,
	repository.Module,
	engine.Module,
	fx.Provide(preview.NewManager),
	service.Module,
	handler.Module,
	httpapi.Module,
	route.Module,
)
