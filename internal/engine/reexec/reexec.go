// Package reexec lets the single sandboxd binary act as its own sandbox init
// (PID 1 inside the namespaces) and per-command shim. Go cannot run code
// between fork and exec, so the daemon re-executes itself with a marker
// argv[0] instead. Dispatch must be the first call in every main.
package reexec

const (
	// InitName is argv[0] of the in-sandbox init process.
	InitName = "sandboxd-init"
	// ShimName is argv[0] of the per-command shim that drops privileges,
	// installs seccomp, and execs the target.
	ShimName = "sandboxd-shim"
)

var handlers = map[string]func(){}

// Register associates argv[0] with an entrypoint. It is called from main via
// engine.RegisterReexec rather than init() so registration stays explicit.
func Register(name string, fn func()) {
	handlers[name] = fn
}
