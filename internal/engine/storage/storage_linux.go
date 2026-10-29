//go:build linux

// Package storage provides each sandbox's writable layer: a directory whose
// contents are capped at the sandbox's storage size. The "disk" driver mounts
// a dedicated filesystem from a sparse image file on the state disk, whose
// size is the hard limit; the "tmpfs" driver keeps the layer in memory.
package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Driver names accepted by SANDBOX_STORAGE_DRIVER.
const (
	DriverDisk  = "disk"
	DriverTmpfs = "tmpfs"
)

// Driver manages capped directories under one parent directory.
type Driver interface {
	// Name identifies the mechanism, for logs ("btrfs", "xfs", "tmpfs", ...).
	Name() string
	// ChargesMemory reports whether stored bytes are memory (tmpfs), which
	// the sandbox's memory limit must then make room for.
	ChargesMemory() bool
	// Create makes dir, a new directory whose contents may total at most
	// limitBytes. Writes beyond fail with EDQUOT or ENOSPC.
	Create(dir string, limitBytes int64) error
	// Usage returns the bytes stored under dir. Some drivers account lazily
	// (btrfs updates usage when a transaction commits).
	Usage(dir string) (int64, error)
	// Remove deletes dir and releases its accounting. It also removes
	// directories another driver created, and a missing dir is not an error.
	Remove(dir string) error
}

// Open returns the driver kind ("disk" or "tmpfs"). For disk, fs selects the
// filesystem inside each image ("ext4", "btrfs", or "" for the first one the
// kernel and host tools support).
func Open(kind, fs string) (Driver, error) {
	switch kind {
	case DriverTmpfs:
		return tmpfs{}, nil
	case DriverDisk:
		return newDiskImage(fs)
	default:
		return nil, fmt.Errorf("unknown storage driver %q", kind)
	}
}

// probeLimit and probeWrite size the startup check that a limit holds.
const (
	probeLimit = MinBytes
	probeWrite = probeLimit + 8<<20
)

// MinBytes is the smallest storage any driver supports (btrfs's minimum).
const MinBytes = 16 << 20

// Probe proves the driver works and enforces its limit by writing past a
// small one in a scratch directory under parent. It fails closed: storage
// that does not hold its limit must not run sandboxes.
func Probe(driver Driver, parent string) error {
	dir := filepath.Join(parent, ".storage-probe")
	if err := driver.Remove(dir); err != nil {
		return fmt.Errorf("remove stale probe: %w", err)
	}
	if err := driver.Create(dir, probeLimit); err != nil {
		return fmt.Errorf("%s storage: create a limited directory: %w", driver.Name(), err)
	}
	defer func() { _ = driver.Remove(dir) }()

	file, err := os.Create(filepath.Join(dir, "fill")) // #nosec G304 -- fixed name in the engine's own directory.
	if err != nil {
		return fmt.Errorf("%s storage: %w", driver.Name(), err)
	}
	defer func() { _ = file.Close() }()
	chunk := make([]byte, 64<<10)
	written := 0
	for written < probeWrite {
		n, err := file.Write(chunk)
		written += n
		if errors.Is(err, unix.EDQUOT) || errors.Is(err, unix.ENOSPC) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s storage: probe write: %w", driver.Name(), err)
		}
	}
	if err := file.Sync(); errors.Is(err, unix.EDQUOT) || errors.Is(err, unix.ENOSPC) {
		return nil
	}
	return fmt.Errorf("%s storage: the limit is not enforced (wrote %d bytes into a %d-byte directory)", driver.Name(), written, probeLimit)
}

// removeTree unmounts anything mounted at dir (a tmpfs from the tmpfs
// driver) and deletes it.
func removeTree(dir string) error {
	if err := unix.Unmount(dir, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("unmount %s: %w", dir, err)
	}
	return os.RemoveAll(dir)
}
