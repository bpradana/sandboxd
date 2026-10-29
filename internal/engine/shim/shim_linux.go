//go:build linux

// Package shim is the last step before a sandbox command runs. Init starts it
// as the unprivileged sandbox user in a new session; the shim resets its OOM
// priority, enters the working directory, sets no_new_privs, installs the
// seccomp filter on all threads, and execve()s the target. Go cannot run code
// between fork and exec, which is why this is a separate re-execution.
package shim

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/engine/seccomp"
)

// Exit codes follow shell conventions.
const (
	exitCannotRun = 126
	exitNotFound  = 127
)

// Main is the entrypoint of a re-executed "sandboxd-shim":
//
//	sandboxd-shim <cwd> <seccomp 0|1> -- <argv...>
//
// It never returns.
func Main() {
	// no_new_privs is per thread and execve runs on the calling thread, so
	// everything from here to execve stays on one OS thread.
	runtime.LockOSThread()
	args := os.Args
	if len(args) < 5 || args[3] != "--" {
		fail(exitCannotRun, "invalid shim invocation")
	}
	cwd, withSeccomp, argv := args[1], args[2] == "1", args[4:]

	// Init runs with a protective oom_score_adj; commands must not inherit
	// it, so a runaway command is what the OOM killer picks.
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("0"), 0o644) // #nosec G306 -- procfs.

	if cwd != "" {
		if err := unix.Chdir(cwd); err != nil {
			fail(exitCannotRun, fmt.Sprintf("cannot enter working directory %s: %v", cwd, err))
		}
	}

	if withSeccomp {
		if err := seccomp.Install(); err != nil {
			fail(exitCannotRun, err.Error())
		}
	} else if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		fail(exitCannotRun, fmt.Sprintf("set no_new_privs: %v", err))
	}

	path, err := exec.LookPath(argv[0])
	if err != nil {
		code := exitCannotRun
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			code = exitNotFound
		}
		fail(code, fmt.Sprintf("%s: command not found", argv[0]))
	}
	resetSignals()
	err = unix.Exec(path, argv, os.Environ())
	code := exitCannotRun
	if errors.Is(err, unix.ENOENT) {
		code = exitNotFound
	}
	fail(code, fmt.Sprintf("%s: %v", argv[0], err))
}

// resetSignals gives the command default signal dispositions. An ignored
// disposition survives execve, and the daemon may have inherited some from
// whatever started it (Go keeps signals that were ignored at startup
// ignored); without this, Ctrl-C, kill, or a closed pipe could fail to stop
// a command. Caught signals are reset by execve itself, so resetting every
// signal just before it changes nothing for the shim.
func resetSignals() {
	// An all-zero kernel sigaction is SIG_DFL with no flags and an empty mask
	// on every architecture's layout.
	var action [4]uint64
	for sig := 1; sig <= 64; sig++ {
		if sig == int(unix.SIGKILL) || sig == int(unix.SIGSTOP) {
			continue
		}
		_, _, _ = unix.RawSyscall6(unix.SYS_RT_SIGACTION, uintptr(sig), uintptr(unsafe.Pointer(&action)), 0, 8, 0, 0) // #nosec G103 -- kernel ABI call.
	}
}

func fail(code int, message string) {
	_, _ = fmt.Fprintln(os.Stderr, "sandboxd: "+message)
	os.Exit(code)
}
