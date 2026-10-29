//go:build linux

// Package seccomp installs the syscall filter applied to every command in a
// sandbox. The policy is a deny-list on top of the user namespace, dropped
// capabilities, and no_new_privs: it removes kernel interfaces that are rarely
// needed by agent workloads but frequently involved in container escapes.
package seccomp

import (
	"fmt"

	libseccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
)

// denied syscalls return EPERM. Names missing on the running architecture
// (for example iopl on arm64) are skipped.
var denied = []string{
	// Tracing other processes and reading their memory.
	"ptrace", "process_vm_readv", "process_vm_writev", "kcmp",
	// Mount table and namespace manipulation.
	"mount", "umount", "umount2", "pivot_root", "chroot",
	"unshare", "setns",
	"open_tree", "move_mount", "fsopen", "fsconfig", "fsmount", "fspick", "mount_setattr",
	// Kernel attack surface rarely used by workloads.
	"bpf", "perf_event_open", "userfaultfd", "io_uring_setup", "io_uring_enter", "io_uring_register",
	"keyctl", "add_key", "request_key",
	"open_by_handle_at", "name_to_handle_at",
	"init_module", "finit_module", "delete_module",
	"kexec_load", "kexec_file_load", "reboot",
	"swapon", "swapoff", "acct", "quotactl", "quotactl_fd",
	"iopl", "ioperm", "syslog", "lookup_dcookie",
	"settimeofday", "clock_settime", "clock_adjtime", "adjtimex", "stime",
	"vhangup", "nfsservctl", "uselib",
}

// Policy returns the filter for the running architecture.
func Policy() (libseccomp.Filter, error) {
	info, err := arch.GetInfo("")
	if err != nil {
		return libseccomp.Filter{}, fmt.Errorf("seccomp arch: %w", err)
	}

	names := make([]string, 0, len(denied))
	for _, name := range denied {
		if _, ok := info.SyscallNames[name]; ok {
			names = append(names, name)
		}
	}

	return libseccomp.Filter{
		NoNewPrivs: true,
		Flag:       libseccomp.FilterFlagTSync,
		Policy: libseccomp.Policy{
			DefaultAction: libseccomp.ActionAllow,
			Syscalls: []libseccomp.SyscallGroup{
				{Action: libseccomp.ActionErrno, Names: names},
			},
		},
	}, nil
}

// Install sets no_new_privs and loads the filter on every thread of the
// calling process. The filter is inherited across execve and fork.
func Install() error {
	filter, err := Policy()
	if err != nil {
		return err
	}
	if err := libseccomp.LoadFilter(filter); err != nil {
		return fmt.Errorf("load seccomp filter: %w", err)
	}
	return nil
}
