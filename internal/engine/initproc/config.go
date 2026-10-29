// Package initproc is the PID-1 process inside every sandbox. It finishes the
// sandbox's mount namespace, drops what it does not need, and then serves the
// agent protocol: spawning commands through the shim, reaping every child
// (including orphans reparented to it), enforcing timeouts by killing whole
// process groups, and holding persistent shell sessions.
package initproc

// File descriptors the daemon passes to init (ExtraFiles start at 3).
const (
	FDListener = 3
	FDConfig   = 4
	FDStatus   = 5
	// FDMemoryCurrent and FDMemoryStat are read-only descriptors of the
	// sandbox cgroup's memory.current and memory.stat.
	FDMemoryCurrent = 6
	FDMemoryStat    = 7
)

// StatusOK is written to the status pipe once init is ready to serve.
const StatusOK = "ok"

// Config is sent by the daemon over the config pipe after the network is in
// place. Its arrival is also the signal that host-side setup is complete.
type Config struct {
	// Rootfs is the overlay mount, as a path in the copied host mount table.
	Rootfs   string `json:"rootfs"`
	Hostname string `json:"hostname"`
	Seccomp  bool   `json:"seccomp"`
	// MemoryLimitBytes is the cgroup's memory.max, shown as MemTotal.
	MemoryLimitBytes int64 `json:"memory_limit_bytes"`
	// MaxTerminals caps live terminals, attached or not.
	MaxTerminals int `json:"max_terminals"`
	// TerminalDetachMs is how long a terminal survives without a client.
	TerminalDetachMs int64 `json:"terminal_detach_ms"`
}
