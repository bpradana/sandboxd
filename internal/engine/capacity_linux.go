//go:build linux

package engine

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// HostCapacity reports what this machine has, as opposed to what the daemon
// has handed out. A placement layer needs both: the allocated side says what
// has been promised, and this says whether the promise can be kept.
func (e *linuxEngine) HostCapacity() (*HostCapacity, error) {
	capacity := &HostCapacity{
		CPUMillis:    runtime.NumCPU() * 1000,
		Architecture: runtime.GOARCH,
	}
	total, available, err := memoryKB()
	if err != nil {
		return nil, err
	}
	capacity.MemoryMB = total >> 10
	capacity.MemoryAvailableMB = available >> 10

	var fs unix.Statfs_t
	if err := unix.Statfs(e.sandboxCfg.StateDir, &fs); err != nil {
		return nil, err
	}
	// Bavail, not Bfree: the daemon is not root on every filesystem it is
	// given, and the reserved blocks are not its to spend.
	capacity.StorageFreeMB = int(fs.Bavail * uint64(fs.Bsize) >> 20)  // #nosec G115 -- block counts are non-negative.
	capacity.StorageTotalMB = int(fs.Blocks * uint64(fs.Bsize) >> 20) // #nosec G115 -- block counts are non-negative.
	return capacity, nil
}

// memoryKB reads MemTotal and MemAvailable. MemAvailable is the kernel's own
// estimate of what can be allocated without swapping, which is a far better
// guide than free memory on a host that has been running for a while.
func memoryKB() (total, available int, err error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		if name != "MemTotal" && name != "MemAvailable" {
			continue
		}
		parsed, convErr := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(value), " kB"))
		if convErr != nil {
			continue
		}
		if name == "MemTotal" {
			total = parsed
		} else {
			available = parsed
		}
	}
	return total, available, scanner.Err()
}
