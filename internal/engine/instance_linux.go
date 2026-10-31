//go:build linux

package engine

import (
	"bufio"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine/fsops"
	"github.com/bpradana/sandboxd/internal/engine/initproc"
	"github.com/bpradana/sandboxd/internal/engine/netns"
	"github.com/bpradana/sandboxd/internal/engine/reexec"
	"github.com/bpradana/sandboxd/internal/engine/storage"
)

const (
	sandboxUser = "sandbox"
	sandboxHome = "/home/sandbox"
	rootHome    = "/root"
	// selfFDMin keeps the sealed binary's fd above the range exec.Cmd uses
	// for ExtraFiles, so it is never clobbered before execve resolves it.
	selfFDMin = 100
)

// instance is one sandbox's kernel-level state.
type instance struct {
	id   string
	spec Spec
	dir  string

	// lower is the read-only layer under the sandbox's overlay: the base
	// image, or the private tree a restored snapshot was imported into.
	lower string

	cmd        *exec.Cmd
	pid        int
	attachment *netns.Attachment
	configPipe *os.File
	statusPipe *os.File

	execSem    chan struct{}
	sessionsMu sync.Mutex
	sessions   map[string]struct{}

	done chan struct{}
	// started is set once Create returned successfully; exits before that
	// are reported through Create's error, not the Exits channel.
	started      atomic.Bool
	destroying   atomic.Bool
	snapshotting atomic.Bool
	teardownMu   sync.Mutex
	// fsMu drains host-side file operations before a filesystem capture.
	fsMu sync.RWMutex
}

func newInstance(spec Spec, dir string, maxConcurrent int) *instance {
	return &instance{
		id:       spec.ID,
		spec:     spec,
		dir:      dir,
		execSem:  make(chan struct{}, maxConcurrent),
		sessions: make(map[string]struct{}),
		done:     make(chan struct{}),
	}
}

func (i *instance) scratchDir() string { return filepath.Join(i.dir, "scratch") }

func (i *instance) rootfsDir() string { return filepath.Join(i.dir, "rootfs") }

func (i *instance) socketPath() string { return filepath.Join(i.dir, "agent.sock") }

func (i *instance) initConfig(cfg config.SandboxConfig, execCfg config.ExecConfig, memoryStorageMB int) initproc.Config {
	return initproc.Config{
		Rootfs:           i.rootfsDir(),
		Hostname:         i.spec.Hostname,
		Seccomp:          cfg.SeccompEnabled,
		MemoryLimitBytes: int64(i.spec.Limits.MemoryMB+memoryStorageMB) << 20,
		MaxTerminals:     execCfg.MaxTerminals,
		TerminalDetachMs: execCfg.TerminalDetachTimeout.Milliseconds(),
	}
}

func (i *instance) closePipes() {
	for _, pipe := range []**os.File{&i.configPipe, &i.statusPipe} {
		if *pipe != nil {
			_ = (*pipe).Close()
			*pipe = nil
		}
	}
}

// mountStorage mounts a tmpfs capped at storageMB and an overlay of the image
// with its upper/work directories on that tmpfs. Both live in the daemon's
// mount namespace (under a private mount) so file operations can reach them.
// mountStorage creates the capped writable layer and mounts the overlay of
// it over the image.
func (e *linuxEngine) mountStorage(inst *instance, lower string) error {
	scratch := inst.scratchDir()
	uidBase := e.sandboxCfg.UIDMapBase
	if err := e.storage.Create(scratch, storage.Options{
		Size:        int64(inst.spec.Limits.StorageMB) << 20,
		MemoryBytes: int64(inst.spec.Limits.MemoryMB) << 20,
	}); err != nil {
		return fmt.Errorf("create %s storage: %w", e.storage.Name(), err)
	}

	upper, work := filepath.Join(scratch, "upper"), filepath.Join(scratch, "work")
	for _, dir := range []string{upper, work} {
		if err := os.Mkdir(dir, 0o755); err != nil { // #nosec G301 -- upper becomes the sandbox's "/".
			return err
		}
	}
	// The upper root becomes the sandbox's "/", owned by sandbox root.
	if err := os.Lchown(upper, uidBase, uidBase); err != nil {
		return err
	}

	rootfs := inst.rootfsDir()
	if err := os.Mkdir(rootfs, 0o755); err != nil { // #nosec G301 -- overlay mount point reached by sandbox root.
		return err
	}
	options := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lower, upper, work)
	if err := unix.Mount("overlay", rootfs, "overlay", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}
	return nil
}

// prepareRootfs writes the files every sandbox needs before init starts:
// hostname, hosts, resolv.conf, the sandbox user, and mount points. All writes
// go through openat2-confined fsops even though the tree is still pristine.
func prepareRootfs(inst *instance, cfg config.SandboxConfig, dns []string) error {
	rootUID := cfg.UIDMapBase
	userUID, userGID := cfg.UIDMapBase+cfg.ExecUID, cfg.UIDMapBase+cfg.ExecGID

	asRoot, err := fsops.OpenRoot(inst.rootfsDir(), rootUID, rootUID)
	if err != nil {
		return err
	}
	defer func() { _ = asRoot.Close() }()

	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{"/proc", 0o555},
		{"/sys", 0o555},
		{"/dev", 0o755},
		{"/etc", 0o755},
		{"/tmp", 0o777 | os.ModeSticky},
		{"/home", 0o755},
		{rootHome, 0o700},
	} {
		if _, err := asRoot.Mkdir(dir.path, dir.mode, true); err != nil {
			return fmt.Errorf("create %s: %w", dir.path, err)
		}
	}

	hostname := inst.spec.Hostname
	files := map[string]string{
		"/etc/hostname": hostname + "\n",
		"/etc/hosts":    "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n127.0.1.1\t" + hostname + "\n",
	}
	if len(dns) > 0 {
		var resolv strings.Builder
		for _, server := range dns {
			resolv.WriteString("nameserver " + server + "\n")
		}
		files["/etc/resolv.conf"] = resolv.String()
	} else {
		files["/etc/resolv.conf"] = ""
	}
	for name, content := range files {
		if _, err := asRoot.WriteFile(name, 0o644, strings.NewReader(content), 1<<20); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	if err := ensureAccount(asRoot, "/etc/passwd", sandboxUser, cfg.ExecUID,
		fmt.Sprintf("%s:x:%d:%d:sandbox:%s:/bin/sh\n", sandboxUser, cfg.ExecUID, cfg.ExecGID, sandboxHome)); err != nil {
		return err
	}
	if err := ensureAccount(asRoot, "/etc/group", sandboxUser, cfg.ExecGID,
		fmt.Sprintf("%s:x:%d:\n", sandboxUser, cfg.ExecGID)); err != nil {
		return err
	}

	asUser, err := fsops.OpenRoot(inst.rootfsDir(), userUID, userGID)
	if err != nil {
		return err
	}
	defer func() { _ = asUser.Close() }()
	if _, err := asUser.Mkdir(sandboxHome, 0o755, true); err != nil {
		return fmt.Errorf("create %s: %w", sandboxHome, err)
	}
	return nil
}

// ensureAccount appends line to an /etc/passwd-style file unless an entry for
// name or id already exists.
func ensureAccount(root *fsops.Root, file, name string, id int, line string) error {
	existing, err := root.ReadFile(file, 4<<20)
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("read %s: %w", file, err)
	}
	idText := strconv.Itoa(id)
	for _, entry := range strings.Split(string(existing), "\n") {
		fields := strings.Split(entry, ":")
		if len(fields) >= 3 && (fields[0] == name || fields[2] == idText) {
			return nil
		}
	}
	content := string(existing)
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if _, err := root.WriteFile(file, 0o644, strings.NewReader(content+line), 4<<20); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	return nil
}

// launchInit starts the sealed binary as "sandboxd-init" in new user, mount,
// pid, net, uts, ipc, and cgroup namespaces, born directly inside the sandbox
// cgroup via clone3(CLONE_INTO_CGROUP).
func (e *linuxEngine) launchInit(ctx context.Context, inst *instance, cgroupFile *os.File) error {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: inst.socketPath(), Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen agent socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	listenerFile, err := listener.File()
	_ = listener.Close()
	if err != nil {
		return err
	}
	defer func() { _ = listenerFile.Close() }()
	memoryCurrent, memoryStat, err := e.cgroups.OpenMemoryFiles(inst.id)
	if err != nil {
		return fmt.Errorf("open sandbox memory accounting: %w", err)
	}
	defer closeFiles(memoryCurrent, memoryStat)
	if err := os.Chmod(inst.socketPath(), 0o600); err != nil {
		return err
	}

	configR, configW, err := os.Pipe()
	if err != nil {
		return err
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		closeFiles(configR, configW)
		return err
	}
	logR, logW, err := os.Pipe()
	if err != nil {
		closeFiles(configR, configW, statusR, statusW)
		return err
	}

	size := e.sandboxCfg.UIDMapSize
	base := e.sandboxCfg.UIDMapBase
	cmd := &exec.Cmd{
		Path:   fmt.Sprintf("/proc/self/fd/%d", e.self.Fd()),
		Args:   []string{reexec.InitName},
		Env:    []string{},
		Stdout: logW,
		Stderr: logW,
		// Order fixes the descriptor numbers in initproc (FDListener...).
		ExtraFiles: []*os.File{listenerFile, configR, statusW, memoryCurrent, memoryStat},
		SysProcAttr: &syscall.SysProcAttr{
			Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET |
				unix.CLONE_NEWUTS | unix.CLONE_NEWIPC | unix.CLONE_NEWCGROUP,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: base, Size: size}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: base, Size: size}},
			GidMappingsEnableSetgroups: true,
			// Host uid 0 is not mapped into the namespace; without switching
			// to namespace uid 0 (host UIDMapBase) init would exec as the
			// overflow uid with no capabilities.
			Credential:  &syscall.Credential{Uid: 0, Gid: 0},
			UseCgroupFD: true,
			CgroupFD:    int(cgroupFile.Fd()), // #nosec G115 -- valid descriptor.
			Setsid:      true,
		},
	}
	if err := ctx.Err(); err != nil {
		closeFiles(configR, configW, statusR, statusW, logR, logW)
		return err
	}
	startErr := cmd.Start()
	closeFiles(configR, statusW, logW)
	if startErr != nil {
		closeFiles(configW, statusR, logR)
		return fmt.Errorf("start sandbox init: %w", startErr)
	}

	inst.cmd = cmd
	inst.pid = cmd.Process.Pid
	inst.configPipe = configW
	inst.statusPipe = statusR
	go e.forwardInitLog(inst.id, logR)
	go e.watch(inst)

	if err := os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", inst.pid), []byte(initOOMScoreAdj), 0o644); err != nil { // #nosec G306 -- procfs.
		e.logger.Warn("protect sandbox init from oom killer", zap.String("sandbox_id", inst.id), zap.Error(err))
	}
	return nil
}

func (e *linuxEngine) forwardInitLog(id string, src io.ReadCloser) {
	defer func() { _ = src.Close() }()
	scanner := bufio.NewScanner(src)
	for scanner.Scan() {
		e.logger.Warn("sandbox init output", zap.String("sandbox_id", id), zap.String("line", scanner.Text()))
	}
}

// removeSandboxDir unmounts a sandbox's rootfs, removes its storage through
// driver (nil: as a plain tree), and deletes its directory. It refuses to
// delete while anything is still mounted beneath, which would otherwise
// recurse into the sandbox filesystem.
func removeSandboxDir(driver storage.Driver, dir string) error {
	rootfs := filepath.Join(dir, "rootfs")
	if err := unix.Unmount(rootfs, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("unmount %s: %w", rootfs, err)
	}
	scratch := filepath.Join(dir, "scratch")
	if driver == nil {
		if err := unix.Unmount(scratch, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("unmount %s: %w", scratch, err)
		}
	} else if err := driver.Remove(scratch); err != nil {
		return fmt.Errorf("remove storage: %w", err)
	}
	mounted, err := mountedUnder(dir)
	if err != nil {
		return err
	}
	if mounted {
		return fmt.Errorf("refusing to remove %s: filesystems still mounted beneath it", dir)
	}
	return os.RemoveAll(dir)
}

func mountedUnder(dir string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	prefix := filepath.Clean(dir) + "/"
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 4 && strings.HasPrefix(unescapeMountPath(fields[4])+"/", prefix) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func unescapeMountPath(value string) string {
	replacer := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(value)
}

// makePrivateMount turns dir into its own mount with private propagation, so
// per-sandbox mounts never propagate to other namespaces (e.g. the host's
// shared root on systemd machines).
func makePrivateMount(dir string) error {
	mounted, err := isMountPoint(dir)
	if err != nil {
		return err
	}
	if !mounted {
		if err := unix.Mount(dir, dir, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind %s: %w", dir, err)
		}
	}
	if err := unix.Mount("", dir, "", unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

func isMountPoint(dir string) (bool, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	clean := filepath.Clean(dir)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 4 && unescapeMountPath(fields[4]) == clean {
			return true, nil
		}
	}
	return false, scanner.Err()
}

// sealedSelf copies the running executable into a sealed memfd. Sandboxes are
// launched from it, so /proc/<pid>/exe inside a sandbox points at an
// immutable in-memory copy rather than the host binary (CVE-2019-5736).
func sealedSelf() (*os.File, error) {
	src, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, err
	}
	defer func() { _ = src.Close() }()

	fd, err := unix.MemfdCreate("sandboxd", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	memfd := os.NewFile(uintptr(fd), "sandboxd-sealed") // #nosec G115 -- valid descriptor.
	if _, err := io.Copy(memfd, src); err != nil {
		_ = memfd.Close()
		return nil, err
	}
	seals := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err := unix.FcntlInt(memfd.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		_ = memfd.Close()
		return nil, fmt.Errorf("seal memfd: %w", err)
	}
	high, err := unix.FcntlInt(memfd.Fd(), unix.F_DUPFD_CLOEXEC, selfFDMin)
	_ = memfd.Close()
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(high), "sandboxd-sealed"), nil // #nosec G115 -- valid descriptor.
}

// preflight checks the host can run sandboxes before anything is changed.
func preflight() error {
	if os.Geteuid() != 0 {
		return errors.New("sandboxd must run as root")
	}
	if err := fsops.Supported(); err != nil {
		return err
	}
	filesystems, err := os.ReadFile("/proc/filesystems")
	if err != nil {
		return err
	}
	for _, fs := range []string{"overlay", "tmpfs", "cgroup2"} {
		if !strings.Contains(string(filesystems), "\t"+fs+"\n") {
			return fmt.Errorf("kernel filesystem %q is not available", fs)
		}
	}
	return requireStaticBinary()
}

// requireStaticBinary rejects a dynamically linked build: the binary re-execs
// itself as the shim inside sandbox images that do not carry the host's libc
// or dynamic loader, so it must be built with CGO_ENABLED=0.
func requireStaticBinary() error {
	file, err := elf.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("inspect executable: %w", err)
	}
	defer func() { _ = file.Close() }()
	for _, prog := range file.Progs {
		if prog.Type == elf.PT_INTERP {
			return errors.New("sandboxd must be statically linked (build with CGO_ENABLED=0)")
		}
	}
	return nil
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}
