//go:build linux

// Package rootfs builds a sandbox's mount namespace from inside it: private
// propagation, /proc, a minimal /dev, masked and read-only kernel interfaces,
// and finally pivot_root into the overlay so the host tree is unreachable.
// It runs in the sandbox init process, which is root only inside its user
// namespace.
package rootfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	procFlags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
)

// devices are bind-mounted from the host; device nodes cannot be created
// inside a user namespace, and nothing else is exposed.
var devices = []string{"null", "zero", "full", "random", "urandom", "tty"}

// maskedFiles are hidden behind /dev/null; maskedDirs behind an empty
// read-only tmpfs. readOnly paths stay visible but cannot be written.
var (
	maskedFiles = []string{
		"/proc/kcore", "/proc/keys", "/proc/latency_stats", "/proc/timer_list",
		"/proc/timer_stats", "/proc/sched_debug", "/proc/kallsyms", "/proc/kmsg",
	}
	maskedDirs = []string{"/proc/acpi", "/proc/scsi", "/sys/firmware", "/sys/kernel/security"}
	readOnly   = []string{"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger"}
)

// Setup prepares root (a host path visible in this mount namespace) and
// pivots into it. sysctl writes that must happen while /proc/sys is still
// writable run in beforeReadOnly.
func Setup(root string, beforeReadOnly func()) error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make mounts private: %w", err)
	}
	if err := unix.Mount(root, root, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("bind rootfs: %w", err)
	}

	if err := mountAt(root, "/proc", "proc", "proc", procFlags, ""); err != nil {
		return err
	}
	// sysfs needs a network namespace owned by this user namespace, which we
	// have; it is read-only and optional, so failure is not fatal.
	if err := mountAt(root, "/sys", "sysfs", "sysfs", procFlags|unix.MS_RDONLY, ""); err == nil {
		// The sandbox's own cgroup (the cgroup namespace root), read-only:
		// JVMs, Node, Go, and other runtimes size themselves from its limits.
		// Best effort, like sysfs.
		_ = mountAt(root, "/sys/fs/cgroup", "cgroup2", "cgroup2", procFlags|unix.MS_RDONLY, "")
	}

	if err := setupDev(root); err != nil {
		return err
	}
	if beforeReadOnly != nil {
		beforeReadOnly()
	}
	if err := maskPaths(root); err != nil {
		return err
	}
	return pivot(root)
}

// target returns root+rel after checking the existing path is not a symlink,
// so an image cannot redirect a mount onto an unexpected location.
func target(root, rel string, dir bool) (string, error) {
	full := filepath.Join(root, rel)
	info, err := os.Lstat(full)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if dir {
			return full, os.MkdirAll(full, 0o755) // #nosec G301 -- mount point inside the sandbox root.
		}
		file, err := os.OpenFile(full, os.O_CREATE|os.O_WRONLY, 0o644) // #nosec G304,G302 -- bind-mount target inside the sandbox root.
		if err != nil {
			return "", err
		}
		return full, file.Close()
	case err != nil:
		return "", err
	case info.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("mount target %s is a symlink", rel)
	}
	return full, nil
}

func mountAt(root, rel, source, fstype string, flags uintptr, data string) error {
	dst, err := target(root, rel, true)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", rel, err)
	}
	if err := unix.Mount(source, dst, fstype, flags, data); err != nil {
		return fmt.Errorf("mount %s on %s: %w", fstype, rel, err)
	}
	return nil
}

func setupDev(root string) error {
	if err := mountAt(root, "/dev", "tmpfs", "tmpfs", unix.MS_NOSUID|unix.MS_STRICTATIME, "mode=755,size=65536k"); err != nil {
		return err
	}
	for _, name := range devices {
		dst, err := target(root, "/dev/"+name, false)
		if err != nil {
			return err
		}
		if err := unix.Mount("/dev/"+name, dst, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind /dev/%s: %w", name, err)
		}
	}
	if err := mountAt(root, "/dev/pts", "devpts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"); err != nil {
		return err
	}
	if err := mountAt(root, "/dev/shm", "shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "mode=1777,size=65536k"); err != nil {
		return err
	}
	links := map[string]string{
		"/dev/ptmx":   "pts/ptmx",
		"/dev/fd":     "/proc/self/fd",
		"/dev/stdin":  "/proc/self/fd/0",
		"/dev/stdout": "/proc/self/fd/1",
		"/dev/stderr": "/proc/self/fd/2",
	}
	for link, dest := range links {
		if err := os.Symlink(dest, filepath.Join(root, link)); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("symlink %s: %w", link, err)
		}
	}
	return nil
}

func maskPaths(root string) error {
	for _, rel := range maskedFiles {
		dst := filepath.Join(root, rel)
		if _, err := os.Lstat(dst); err != nil {
			continue
		}
		if err := unix.Mount("/dev/null", dst, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("mask %s: %w", rel, err)
		}
	}
	for _, rel := range maskedDirs {
		dst := filepath.Join(root, rel)
		if info, err := os.Lstat(dst); err != nil || !info.IsDir() {
			continue
		}
		if err := unix.Mount("tmpfs", dst, "tmpfs", unix.MS_RDONLY|procFlags, "size=0"); err != nil {
			return fmt.Errorf("mask %s: %w", rel, err)
		}
	}
	for _, rel := range readOnly {
		dst := filepath.Join(root, rel)
		if _, err := os.Lstat(dst); err != nil {
			continue
		}
		if err := unix.Mount(dst, dst, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("bind %s: %w", rel, err)
		}
		// Remounts inside a user namespace must keep the locked nosuid,
		// nodev, and noexec flags of the underlying proc mount.
		if err := unix.Mount(dst, dst, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|procFlags, ""); err != nil {
			return fmt.Errorf("make %s read-only: %w", rel, err)
		}
	}
	return nil
}

// pivot switches the root to newRoot and detaches the old root entirely.
func pivot(newRoot string) error {
	oldfd, err := unix.Open("/", unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(oldfd) }()
	newfd, err := unix.Open(newRoot, unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(newfd) }()

	if err := unix.Fchdir(newfd); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Fchdir(oldfd); err != nil {
		return err
	}
	if err := unix.Mount("", ".", "", unix.MS_SLAVE|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("make old root slave: %w", err)
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("detach old root: %w", err)
	}
	return unix.Chdir("/")
}
