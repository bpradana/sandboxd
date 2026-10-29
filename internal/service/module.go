// Package service wires the domain service modules into the application graph.
package service

import (
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/service/sandbox"
)

// Module provides the domain services. Its invoke forces the sandbox service
// (and its reaper hooks) to be built before the HTTP server, so on shutdown
// the server stops first, then the reaper, then the engine tears every
// sandbox down.
var Module = fx.Options(
	fx.Provide(
		sandbox.NewService,
	),
	fx.Invoke(func(sandbox.Service) {}),
)
