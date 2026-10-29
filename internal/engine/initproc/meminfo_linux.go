//go:build linux

package initproc

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// meminfoRefresh is how stale /proc/meminfo inside a sandbox may be.
const meminfoRefresh = time.Second

// virtualMeminfo keeps a file bind-mounted over /proc/meminfo in step with
// the sandbox's cgroup, so top, procps free, and other /proc/meminfo readers
// see the sandbox's memory instead of the host's. The kernel does not
// virtualize /proc/meminfo per cgroup. (sysinfo(2), which busybox free uses
// for its totals, still reports the host.)
type virtualMeminfo struct {
	file    *os.File
	current *os.File
	stat    *os.File
	limit   int64
	buf     []byte
}

// setupMeminfo runs before pivot_root, with root the new root's host path.
// It is best effort: without the cgroup descriptors or the bind mount the
// sandbox sees the host's /proc/meminfo, as any container does.
func setupMeminfo(root string, limit int64) *virtualMeminfo {
	current := os.NewFile(FDMemoryCurrent, "memory.current")
	stat := os.NewFile(FDMemoryStat, "memory.stat")
	// Inherited without CLOEXEC; commands must not inherit them in turn.
	for _, file := range []*os.File{current, stat} {
		if _, err := file.Stat(); err != nil {
			closeAll(current, stat)
			return nil
		}
		syscall.CloseOnExec(int(file.Fd()))
	}
	if limit <= 0 {
		closeAll(current, stat)
		return nil
	}

	// A root-only directory in the sandbox's private /dev tmpfs; the file
	// is reachable for the sandbox user only through /proc/meminfo.
	dir := filepath.Join(root, "dev", ".sandboxd")
	if err := os.Mkdir(dir, 0o700); err != nil && !os.IsExist(err) {
		closeAll(current, stat)
		return nil
	}
	path := filepath.Join(dir, "meminfo")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o444) // #nosec G304 G302 -- fixed path in init's own tmpfs; world-readable like /proc/meminfo.
	if err != nil {
		closeAll(current, stat)
		return nil
	}
	info := &virtualMeminfo{file: file, current: current, stat: stat, limit: limit, buf: make([]byte, 16<<10)}
	if !info.refresh() {
		info.close()
		return nil
	}
	target := filepath.Join(root, "proc", "meminfo")
	if err := unix.Mount(path, target, "", unix.MS_BIND, ""); err != nil {
		info.close()
		return nil
	}
	_ = unix.Mount(path, target, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, "")
	return info
}

// run refreshes the file until init exits.
func (m *virtualMeminfo) run() {
	ticker := time.NewTicker(meminfoRefresh)
	defer ticker.Stop()
	for range ticker.C {
		m.refresh()
	}
}

func (m *virtualMeminfo) refresh() bool {
	current, ok := m.read(m.current)
	if !ok {
		return false
	}
	usage, err := parseInt(current)
	if err != nil {
		return false
	}
	stat, ok := m.read(m.stat)
	if !ok {
		return false
	}
	// Same size every time: an in-place write never shows a reader a
	// truncated file.
	_, err = m.file.WriteAt(formatMeminfo(m.limit, usage, parseMemoryStat(stat)), 0)
	return err == nil
}

func (m *virtualMeminfo) read(file *os.File) ([]byte, bool) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}
	n, err := file.Read(m.buf)
	if err != nil && err != io.EOF {
		return nil, false
	}
	return m.buf[:n], true
}

func (m *virtualMeminfo) close() {
	closeAll(m.file, m.current, m.stat)
}
