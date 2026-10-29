// Package http wires the per-domain HTTP handler modules into the application graph.
package http

import (
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/handler/http/sandbox"
)

var Module = fx.Options(
	fx.Provide(
		sandbox.NewHandler,
	),
)
