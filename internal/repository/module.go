// Package repository wires the domain repositories into the application graph.
package repository

import (
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/repository/sandbox"
)

var Module = fx.Options(
	fx.Provide(
		sandbox.NewRepository,
	),
)
