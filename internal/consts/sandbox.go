// Package consts holds the shared, stable string values of the sandbox domain.
package consts

// Sandbox lifecycle states. A sandbox only moves forward:
// creating → running → stopping → stopped, or running → snapshotting → stopped.
// Failed snapshots retain their filesystem and can retry snapshotting.
const (
	SandboxStateCreating     = "creating"
	SandboxStateRunning      = "running"
	SandboxStateSnapshotting = "snapshotting"
	SandboxStateStopping     = "stopping"
	SandboxStateStopped      = "stopped"
	SandboxStateFailed       = "failed"
)

// Network modes. NetworkNone keeps only loopback; NetworkEgress adds a NATed
// veth with private, link-local, and host destinations blocked.
const (
	NetworkNone   = "none"
	NetworkEgress = "egress"
)

// Session states.
const (
	SessionStateIdle       = "idle"
	SessionStateBusy       = "busy"
	SessionStateTerminated = "terminated"
)

// Reasons recorded when the service tears a sandbox down.
const (
	StopReasonDeleted  = "deleted"
	StopReasonExpired  = "expired"
	StopReasonIdle     = "idle"
	StopReasonShutdown = "shutdown"
	StopReasonExited   = "init_exited"
	StopReasonSnapshot = "snapshot"
)

// File types reported by the file API.
const (
	FileTypeFile    = "file"
	FileTypeDir     = "dir"
	FileTypeSymlink = "symlink"
	FileTypeOther   = "other"
)

// Exec kinds and outcomes used as low-cardinality metric labels.
const (
	ExecKindExec     = "exec"
	ExecKindStream   = "stream"
	ExecKindSession  = "session"
	ExecKindTerminal = "terminal"

	ExecOutcomeOK      = "ok"
	ExecOutcomeNonZero = "nonzero"
	ExecOutcomeTimeout = "timeout"
	ExecOutcomeError   = "error"
)

// Users commands can run as inside a sandbox.
const (
	// UserRoot is uid 0 of the sandbox's user namespace: root inside, an
	// unprivileged uid on the host, with a reduced capability set.
	UserRoot = "root"
	// UserSandbox is the unprivileged sandbox user (SANDBOX_EXEC_UID).
	UserSandbox = "sandbox"
)
