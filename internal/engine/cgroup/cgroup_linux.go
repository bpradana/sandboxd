//go:build linux

// Package cgroup manages the cgroup v2 subtree the engine owns: one leaf for
// the daemon itself and one child per sandbox with memory, CPU, and pids
// limits. Teardown uses cgroup.kill, so every process a sandbox ever spawned
// dies with it regardless of reparenting or double forks.
package cgroup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	mountPoint = "/sys/fs/cgroup"
	// DaemonLeaf holds the daemon's own processes so the root can delegate
	// controllers (cgroup v2's no-internal-processes rule).
	DaemonLeaf = "daemon"
	// SandboxPrefix names per-sandbox cgroups; reconcile removes any it finds.
	SandboxPrefix = "sbx-"

	cpuPeriodMicros = 100000
	killPoll        = 20 * time.Millisecond
)

var requiredControllers = []string{"cpu", "memory", "pids"}

// Limits are the resources written into a sandbox cgroup.
type Limits struct {
	CPUMillis int
	MemoryMB  int
	// StorageMB is added to the memory ceiling when the writable layer is a
	// tmpfs: its pages are charged to the sandbox, and a full disk must not
	// eat the memory processes were promised. Zero for disk storage.
	StorageMB int
	PidsMax   int
}

type Stats struct {
	MemoryBytes      int64
	MemoryPeakBytes  int64
	MemoryLimitBytes int64
	CPUUsageMicros   int64
	PidsCurrent      int64
	PidsLimit        int64
	OOMKills         int64
}

type Manager struct {
	root string
}

// Setup takes ownership of root (or, when empty, the cgroup this process runs
// in), moves every process in it into the daemon leaf, and enables the
// controllers sandboxes need.
func Setup(root string) (*Manager, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(mountPoint, &st); err != nil {
		return nil, fmt.Errorf("statfs %s: %w", mountPoint, err)
	}
	if st.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errors.New("cgroup v2 unified hierarchy is required at " + mountPoint)
	}

	if root == "" {
		self, err := selfCgroup()
		if err != nil {
			return nil, err
		}
		root = DefaultRoot(self)
	}
	root = filepath.Clean(root)
	if !strings.HasPrefix(root, mountPoint) {
		return nil, fmt.Errorf("cgroup root %q is outside %s", root, mountPoint)
	}

	manager := &Manager{root: root}
	if err := manager.adoptRoot(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Root() string { return m.root }

// DefaultRoot derives the engine's cgroup root from this process's cgroup
// path. A process already in the daemon leaf (a restart in the same cgroup,
// e.g. hot reload or the management CLI) uses the leaf's parent, so the
// hierarchy never nests.
func DefaultRoot(selfPath string) string {
	selfPath = filepath.Clean("/" + selfPath)
	if filepath.Base(selfPath) == DaemonLeaf {
		selfPath = filepath.Dir(selfPath)
	}
	return filepath.Join(mountPoint, selfPath)
}

func selfCgroup() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return rest, nil
		}
	}
	return "", errors.New("no cgroup v2 entry in /proc/self/cgroup")
}

func (m *Manager) adoptRoot() error {
	available, err := os.ReadFile(filepath.Join(m.root, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read cgroup.controllers in %s (is the cgroup delegated?): %w", m.root, err)
	}
	have := strings.Fields(string(available))
	var enable []string
	for _, controller := range requiredControllers {
		if !contains(have, controller) {
			return fmt.Errorf("cgroup controller %q is not available in %s", controller, m.root)
		}
		enable = append(enable, "+"+controller)
	}
	if contains(have, "io") {
		enable = append(enable, "+io")
	}

	leaf := filepath.Join(m.root, DaemonLeaf)
	if err := os.Mkdir(leaf, 0o755); err != nil && !errors.Is(err, os.ErrExist) { // #nosec G301 -- cgroupfs directory; mode is advisory.
		return fmt.Errorf("create daemon cgroup: %w", err)
	}
	// Every process still in the root (the daemon, and in a container whatever
	// started it) must move to a leaf before controllers can be delegated.
	for attempt := 0; ; attempt++ {
		pids, err := readLines(filepath.Join(m.root, "cgroup.procs"))
		if err != nil {
			return fmt.Errorf("read root cgroup.procs: %w", err)
		}
		if len(pids) == 0 {
			break
		}
		if attempt > 50 {
			return fmt.Errorf("processes keep appearing in cgroup root %s: %v", m.root, pids)
		}
		if attempt > 0 {
			// Short-lived processes (e.g. a shell pipeline) may be forking.
			time.Sleep(10 * time.Millisecond)
		}
		for _, pid := range pids {
			// A process may exit between listing and moving; ignore ESRCH.
			if err := writeFile(filepath.Join(leaf, "cgroup.procs"), pid); err != nil && !errors.Is(err, unix.ESRCH) {
				return fmt.Errorf("move pid %s into daemon cgroup: %w", pid, err)
			}
		}
	}

	if err := writeFile(filepath.Join(m.root, "cgroup.subtree_control"), strings.Join(enable, " ")); err != nil {
		return fmt.Errorf("enable controllers %v in %s: %w", enable, m.root, err)
	}
	return nil
}

// Create makes the sandbox cgroup, applies limits, and returns its path and an
// O_DIRECTORY fd for clone3(CLONE_INTO_CGROUP). The caller closes the fd.
func (m *Manager) Create(id string, limits Limits) (string, *os.File, error) {
	dir := m.Path(id)
	if err := os.Mkdir(dir, 0o755); err != nil { // #nosec G301 -- cgroupfs directory; mode is advisory.
		return "", nil, fmt.Errorf("create sandbox cgroup: %w", err)
	}

	settings := []struct{ file, value string }{
		{"memory.max", strconv.FormatInt(int64(limits.MemoryMB+limits.StorageMB)<<20, 10)},
		// Only the offending process is OOM-killed; init is protected by its
		// oom_score_adj, so one runaway command does not end the sandbox.
		{"memory.oom.group", "0"},
		{"cpu.max", fmt.Sprintf("%d %d", limits.CPUMillis*cpuPeriodMicros/1000, cpuPeriodMicros)},
		{"pids.max", strconv.Itoa(limits.PidsMax)},
	}
	for _, setting := range settings {
		if err := writeFile(filepath.Join(dir, setting.file), setting.value); err != nil {
			_ = os.Remove(dir)
			return "", nil, fmt.Errorf("set %s: %w", setting.file, err)
		}
	}
	// No swap: memory.max must be a hard ceiling. Absent without swap accounting.
	if err := writeFile(filepath.Join(dir, "memory.swap.max"), "0"); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(dir)
		return "", nil, fmt.Errorf("set memory.swap.max: %w", err)
	}

	fd, err := os.Open(dir) // #nosec G304 -- path built from the engine's cgroup root and a generated id.
	if err != nil {
		_ = os.Remove(dir)
		return "", nil, fmt.Errorf("open sandbox cgroup: %w", err)
	}
	return dir, fd, nil
}

// OpenMemoryFiles opens the sandbox's memory.current and memory.stat for
// reading; init derives the sandbox's /proc/meminfo from them.
func (m *Manager) OpenMemoryFiles(id string) (*os.File, *os.File, error) {
	current, err := os.Open(filepath.Join(m.Path(id), "memory.current")) // #nosec G304 -- engine-built cgroup path.
	if err != nil {
		return nil, nil, err
	}
	stat, err := os.Open(filepath.Join(m.Path(id), "memory.stat")) // #nosec G304 -- engine-built cgroup path.
	if err != nil {
		_ = current.Close()
		return nil, nil, err
	}
	return current, stat, nil
}

func (m *Manager) Path(id string) string {
	return filepath.Join(m.root, SandboxPrefix+id)
}

func (m *Manager) Stats(id string) (*Stats, error) {
	dir := m.Path(id)
	stats := &Stats{}
	var err error
	if stats.MemoryBytes, err = readInt(filepath.Join(dir, "memory.current")); err != nil {
		return nil, err
	}
	// memory.peak exists since Linux 5.19.
	stats.MemoryPeakBytes, _ = readInt(filepath.Join(dir, "memory.peak"))
	stats.MemoryLimitBytes, _ = readInt(filepath.Join(dir, "memory.max"))
	stats.PidsCurrent, _ = readInt(filepath.Join(dir, "pids.current"))
	stats.PidsLimit, _ = readInt(filepath.Join(dir, "pids.max"))
	stats.CPUUsageMicros, _ = readKey(filepath.Join(dir, "cpu.stat"), "usage_usec")
	stats.OOMKills, _ = readKey(filepath.Join(dir, "memory.events"), "oom_kill")
	return stats, nil
}

// Destroy kills every process in the sandbox cgroup, waits for it to empty,
// and removes it. A missing cgroup is not an error.
func (m *Manager) Destroy(ctx context.Context, id string) error {
	return destroyDir(ctx, m.Path(id))
}

func destroyDir(ctx context.Context, dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	ticker := time.NewTicker(killPoll)
	defer ticker.Stop()
	for {
		if err := killAll(dir); err != nil {
			return err
		}
		err := unix.Rmdir(dir)
		if err == nil || errors.Is(err, unix.ENOENT) {
			return nil
		}
		if !errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("remove cgroup %s: %w", dir, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cgroup %s did not empty: %w", dir, ctx.Err())
		case <-ticker.C:
		}
	}
}

// killAll uses cgroup.kill (Linux 5.14+) and falls back to signaling every
// listed process.
func killAll(dir string) error {
	err := writeFile(filepath.Join(dir, "cgroup.kill"), "1")
	if err == nil || errors.Is(err, os.ErrNotExist) && !dirExists(dir) {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cgroup.kill: %w", err)
	}
	pids, err := readLines(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		return nil //nolint:nilerr // The cgroup disappeared concurrently, which is the goal.
	}
	for _, pid := range pids {
		if value, err := strconv.Atoi(pid); err == nil {
			_ = unix.Kill(value, unix.SIGKILL)
		}
	}
	return nil
}

// Reconcile destroys sandbox cgroups left behind by a previous process.
func (m *Manager) Reconcile(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), SandboxPrefix) {
			continue
		}
		if err := destroyDir(ctx, filepath.Join(m.root, entry.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, entry.Name())
	}
	return removed, errors.Join(errs...)
}

func writeFile(file, value string) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0) // #nosec G304 -- cgroupfs control file under the engine's root.
	if err != nil {
		return err
	}
	_, err = f.WriteString(value)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func readInt(file string) (int64, error) {
	data, err := os.ReadFile(file) // #nosec G304 -- cgroupfs control file under the engine's root.
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(data))
	if value == "max" {
		return -1, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func readKey(file, key string) (int64, error) {
	f, err := os.Open(file) // #nosec G304 -- cgroupfs control file under the engine's root.
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == key {
			return strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return 0, fmt.Errorf("%s not found in %s", key, file)
}

func readLines(file string) ([]string, error) {
	data, err := os.ReadFile(file) // #nosec G304 -- cgroupfs control file under the engine's root.
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(data)), nil
}

func dirExists(dir string) bool {
	_, err := os.Stat(dir)
	return err == nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// DestroyPath kills and removes one cgroup directory by path.
func DestroyPath(ctx context.Context, dir string) error {
	return destroyDir(ctx, dir)
}
