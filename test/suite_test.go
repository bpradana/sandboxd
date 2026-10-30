//go:build e2e

// Package e2e drives a running sandboxd over HTTP against the real kernel.
// Run it with `mise run docker e2e`, which starts the privileged dev
// container and executes this suite from a sibling container.
package e2e

import "testing"

func TestSandboxdHTTPE2E(t *testing.T) {
	suite := newE2ESuite(t)

	t.Run("health", suite.runHealthCase)
	t.Run("auth", suite.runAuthCase)
	t.Run("identity and isolation", suite.runIsolationCase)
	t.Run("resource limits", suite.runLimitsCase)
	t.Run("network", suite.runNetworkCase)
	t.Run("sessions", suite.runSessionsCase)
	t.Run("files", suite.runFilesCase)
	t.Run("changes", suite.runChangesCase)
	t.Run("streaming", suite.runStreamCase)
	t.Run("terminal", suite.runTerminalCase)
	t.Run("run tabs", suite.runRunCase)
	t.Run("lifecycle", suite.runLifecycleCase)
}
