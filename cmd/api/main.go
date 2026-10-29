// Package main runs the sandboxd API server, assembling the fx application and blocking until shutdown.
package main

import (
	"time"

	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/engine"
	fxmodule "github.com/bpradana/sandboxd/internal/fx"
	"github.com/bpradana/sandboxd/internal/helper/runapp"
)

// stopTimeout must exceed SANDBOX_DESTROY_TIMEOUT so shutdown can tear down
// every sandbox (they are destroyed concurrently). The deployment's
// termination grace period must in turn exceed this.
const stopTimeout = 60 * time.Second

func main() {
	// The binary doubles as sandbox init and per-command shim; those
	// re-executions must not start the API.
	if engine.Reexec() {
		return
	}

	runapp.Run(fx.New(fxmodule.Module, fx.NopLogger, fx.StopTimeout(stopTimeout)))
}
