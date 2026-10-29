//go:build linux

package initproc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/engine/rootfs"
)

// keptCapabilities stay in init's bounding set, and so are all a command
// running as the sandbox's root can hold. Init needs SETUID, SETGID, and KILL;
// the rest let root administer its own filesystem and install packages, like
// a container runtime's defaults minus NET_RAW, MKNOD, SETFCAP, SETPCAP, and
// AUDIT_WRITE. They are capabilities over the sandbox's user namespace only,
// never the host. Everything else (SYS_ADMIN, NET_ADMIN, SYS_PTRACE, ...) is
// dropped for init and, by inheritance, every process in the sandbox.
var keptCapabilities = map[uintptr]bool{
	unix.CAP_SETUID:           true,
	unix.CAP_SETGID:           true,
	unix.CAP_KILL:             true,
	unix.CAP_CHOWN:            true,
	unix.CAP_DAC_OVERRIDE:     true,
	unix.CAP_FOWNER:           true,
	unix.CAP_FSETID:           true,
	unix.CAP_NET_BIND_SERVICE: true,
	unix.CAP_SYS_CHROOT:       true,
}

// Main is the entrypoint of a re-executed "sandboxd-init". It never returns.
func Main() {
	status := os.NewFile(FDStatus, "status")
	cfg, err := setup()
	if err != nil {
		_, _ = fmt.Fprintf(status, "error: %v", err)
		_ = status.Close()
		os.Exit(1)
	}

	inherited := os.NewFile(FDListener, "agent")
	listener, err := net.FileListener(inherited)
	// FileListener dup'ed the descriptor with CLOEXEC; close the inherited one
	// so commands cannot accept on the agent socket. It must be closed through
	// its *os.File: a raw close would leave the File's finalizer to close
	// whatever unrelated descriptor later reuses the number.
	_ = inherited.Close()
	if err != nil {
		_, _ = fmt.Fprintf(status, "error: agent listener: %v", err)
		_ = status.Close()
		os.Exit(1)
	}

	srv := newServer(cfg)
	go srv.reaper.run()

	_, _ = io.WriteString(status, StatusOK)
	_ = status.Close()

	srv.serve(listener)
	os.Exit(0)
}

func setup() (*Config, error) {
	// Signals from inside the sandbox are ignored by PID 1 unless handled;
	// SIGCHLD is consumed by the reaper. These are caught and dropped rather
	// than set to SIG_IGN: an ignored disposition survives execve, so every
	// command would start unable to be interrupted (Ctrl-C), terminated, or
	// stopped by a closed pipe. Caught signals reset to default on exec. The
	// channel is never drained; signal delivery to it does not block.
	signal.Notify(make(chan os.Signal, 1), unix.SIGPIPE, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)

	cfgFile := os.NewFile(FDConfig, "config")
	data, err := io.ReadAll(io.LimitReader(cfgFile, 1<<20))
	_ = cfgFile.Close()
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	var meminfo *virtualMeminfo
	if err := rootfs.Setup(cfg.Rootfs, func() {
		writeSysctls()
		meminfo = setupMeminfo(cfg.Rootfs, cfg.MemoryLimitBytes)
	}); err != nil {
		return nil, err
	}
	if meminfo != nil {
		go meminfo.run()
	}
	// argv[0] is set, but comm comes from the exec path (/proc/self/fd/N).
	_ = os.WriteFile("/proc/self/comm", []byte("sandboxd-init"), 0o644) // #nosec G306 -- procfs.
	if err := unix.Sethostname([]byte(cfg.Hostname)); err != nil {
		return nil, fmt.Errorf("sethostname: %w", err)
	}
	if err := setRlimits(); err != nil {
		return nil, err
	}
	if err := dropBoundingSet(); err != nil {
		return nil, err
	}
	// Non-dumpable: other processes in the sandbox cannot ptrace init or open
	// its /proc/<pid> files, including /proc/1/exe.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return nil, fmt.Errorf("prctl dumpable: %w", err)
	}
	return &cfg, nil
}

// writeSysctls runs while /proc/sys is still writable. Each write is best
// effort: some are per-namespace and some kernels reject them in a user
// namespace.
func writeSysctls() {
	settings := map[string]string{
		// IPv6 is not routed by the egress policy, so disable it outright.
		"/proc/sys/net/ipv6/conf/all/disable_ipv6":     "1",
		"/proc/sys/net/ipv6/conf/default/disable_ipv6": "1",
		// No nested user namespaces (also covers clone3, which seccomp
		// cannot filter by flags).
		"/proc/sys/user/max_user_namespaces": "0",
	}
	for file, value := range settings {
		_ = os.WriteFile(file, []byte(value), 0o644) // #nosec G306 -- procfs sysctl.
	}
}

func setRlimits() error {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return err
	}
	const want = 65536
	if limit.Max > want {
		limit.Max = want
	}
	limit.Cur = limit.Max
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &limit)
}

// dropBoundingSet shrinks the bounding set of every thread of init. The
// bounding set is per thread, and commands are forked from whichever thread
// the Go scheduler picks, so a single-thread prctl would leave root commands
// with full capabilities. AllThreadsSyscall requires a cgo-free build, which
// sandboxd's preflight enforces.
func dropBoundingSet() error {
	lastCap := lastCapability()
	for capability := uintptr(0); capability <= lastCap; capability++ {
		if keptCapabilities[capability] {
			continue
		}
		_, _, errno := syscall.AllThreadsSyscall(unix.SYS_PRCTL, unix.PR_CAPBSET_DROP, capability, 0)
		if errno != 0 && !errors.Is(errno, unix.EINVAL) {
			return fmt.Errorf("drop capability %d: %w", capability, errno)
		}
	}
	return nil
}

// lastCapability reads the kernel's highest capability, falling back to the
// compile-time value.
func lastCapability() uintptr {
	data, err := os.ReadFile("/proc/sys/kernel/cap_last_cap")
	if err != nil {
		return unix.CAP_LAST_CAP
	}
	var value uintptr
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &value); err != nil {
		return unix.CAP_LAST_CAP
	}
	return value
}
