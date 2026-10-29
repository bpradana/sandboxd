// Package runapp runs an fx application like fx.App.Run, but reports start
// and stop failures on stderr: with fx.NopLogger, Run exits silently, which
// hides engine preflight errors from operators.
package runapp

import (
	"context"
	"log"
	"os"

	"go.uber.org/fx"
)

func Run(app *fx.App) {
	if err := app.Err(); err != nil {
		log.Fatalf("failed to initialize application: %v", err)
	}

	startCtx, cancel := context.WithTimeout(context.Background(), app.StartTimeout())
	err := app.Start(startCtx)
	cancel()
	if err != nil {
		log.Fatalf("failed to start application: %v", err)
	}

	signal := <-app.Wait()

	stopCtx, cancel := context.WithTimeout(context.Background(), app.StopTimeout())
	err = app.Stop(stopCtx)
	cancel()
	if err != nil {
		log.Printf("failed to stop application cleanly: %v", err)
		os.Exit(1)
	}
	os.Exit(signal.ExitCode)
}
