//go:build e2e

package e2e

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runLimitsCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{MemoryMB: 64, StorageMB: 32, PidsMax: 32})

	t.Run("timeout kills the whole process group", func(t *testing.T) {
		started := time.Now()
		result := s.exec(t, sandbox.ID, "sleep 100 & sleep 100", 2)
		if !result.TimedOut || result.Signal != "SIGKILL" || time.Since(started) > 10*time.Second {
			t.Fatalf("result = %+v after %s", result, time.Since(started))
		}
		if out := s.mustSucceed(t, sandbox.ID, "ps -o comm | grep -c sleep || true"); out != "0\n" {
			t.Fatalf("sleep processes survived the timeout: %q", out)
		}
	})

	t.Run("memory limit OOM-kills the command, not the sandbox", func(t *testing.T) {
		result := s.exec(t, sandbox.ID, "head -c 200m /dev/zero | tail > /dev/null", 30)
		if result.ExitCode == 0 {
			t.Fatalf("200 MiB allocation succeeded under a 64 MiB limit: %+v", result)
		}
		s.mustSucceed(t, sandbox.ID, "true")
		status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/"+sandbox.ID+"/stats", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		stats := decode[presentation.SandboxStatsResponse](t, envelope)
		// The cgroup ceiling is memory_mb, plus storage_mb with tmpfs storage.
		if stats.OOMKills < 1 || (stats.MemoryLimitBytes != 64<<20 && stats.MemoryLimitBytes != (64+32)<<20) {
			t.Fatalf("stats = %+v", stats)
		}
	})

	t.Run("pids limit contains fork bombs", func(t *testing.T) {
		// The shell aborts once fork fails; the cap, not the loop, ends it.
		result := s.exec(t, sandbox.ID, "for i in $(seq 1 200); do sleep 30 & done; echo spawned-all", 20)
		if strings.Contains(result.Stdout, "spawned-all") || !strings.Contains(result.Stderr, "fork") {
			t.Fatalf("200 processes fit under a pids limit of 32: %+v", result)
		}
		status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/"+sandbox.ID+"/stats", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		if stats := decode[presentation.SandboxStatsResponse](t, envelope); stats.PidsCurrent > 32 || stats.PidsLimit != 32 {
			t.Fatalf("stats = %+v", stats)
		}
		// The bomb's process group was killed with the command.
		s.mustSucceed(t, sandbox.ID, "true")
	})

	t.Run("storage limit returns ENOSPC", func(t *testing.T) {
		result := s.exec(t, sandbox.ID, "dd if=/dev/zero of=/tmp/fill bs=1M count=100", 30)
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "No space left") {
			t.Fatalf("writing 100 MiB under a 32 MiB limit: %+v", result)
		}
		s.mustSucceed(t, sandbox.ID, "rm -f /tmp/fill")
	})

	t.Run("a full disk does not wedge the sandbox", func(t *testing.T) {
		tight := s.createSandbox(t, presentation.CreateSandboxRequest{MemoryMB: 64, StorageMB: 128})
		result := s.exec(t, tight.ID, "dd if=/dev/zero of=/tmp/fill bs=1M count=200", 30)
		if result.ExitCode == 0 || !strings.Contains(result.Stderr, "No space left") {
			t.Fatalf("filling storage: %+v", result)
		}
		s.mustSucceed(t, tight.ID, "rm /tmp/fill && head -c 32m /dev/zero | wc -c")
	})

	t.Run("disk storage holds more than the sandbox's memory", func(t *testing.T) {
		disk := s.createSandbox(t, presentation.CreateSandboxRequest{MemoryMB: 64, StorageMB: 256})
		if limit := s.stats(t, disk.ID).MemoryLimitBytes; limit != 64<<20 {
			t.Skipf("memory limit %d includes storage: the server uses tmpfs storage", limit)
		}
		s.mustSucceed(t, disk.ID, "dd if=/dev/urandom of=/root/big bs=1M count=150 2>/dev/null && sync")
		stats := s.stats(t, disk.ID)
		if stats.OOMKills != 0 || stats.StorageUsedBytes < 150<<20 || stats.StorageLimitBytes != 256<<20 {
			t.Fatalf("after writing 150 MiB with 64 MiB of memory: %+v", stats)
		}
		// Deleting frees the space for new writes.
		s.mustSucceed(t, disk.ID, "rm /root/big && sync && dd if=/dev/zero of=/root/again bs=1M count=150 2>/dev/null")
	})

	t.Run("output is truncated at the cap", func(t *testing.T) {
		result := s.exec(t, sandbox.ID, "head -c 5000000 /dev/zero | tr '\\0' a", 30)
		if !result.StdoutTruncated || len(result.Stdout) >= 5000000 {
			t.Fatalf("stdout %d bytes, truncated=%v", len(result.Stdout), result.StdoutTruncated)
		}
	})

	t.Run("/proc/meminfo shows the sandbox's memory", func(t *testing.T) {
		small := s.createSandbox(t, presentation.CreateSandboxRequest{MemoryMB: 128, StorageMB: 64})
		// MemTotal is the cgroup ceiling: memory, plus storage when it is a
		// tmpfs.
		limit := s.stats(t, small.ID).MemoryLimitBytes
		wantKB := strconv.FormatInt(limit/1024, 10)
		if got := s.mustSucceed(t, small.ID, "awk '/^MemTotal:/ {print $2}' /proc/meminfo"); strings.TrimSpace(got) != wantKB {
			t.Fatalf("MemTotal = %q kB, want %s", got, wantKB)
		}
		// busybox top reads /proc/meminfo: used + free is the ceiling.
		if got := s.mustSucceed(t, small.ID, "top -b -n1 | awk '/^Mem:/ {print $2 + $4}'"); strings.TrimSpace(got) != wantKB {
			t.Fatalf("top used+free = %q kB, want %s", got, wantKB)
		}
		// Runtimes read their limits from the sandbox's cgroup.
		if got := s.mustSucceed(t, small.ID, "cat /sys/fs/cgroup/memory.max"); strings.TrimSpace(got) != strconv.FormatInt(limit, 10) {
			t.Fatalf("/sys/fs/cgroup/memory.max = %q, want %d", got, limit)
		}
		if result := s.exec(t, small.ID, "echo max > /sys/fs/cgroup/memory.max", 10); result.ExitCode == 0 {
			t.Fatal("the sandbox can raise its own memory limit")
		}
		// With tmpfs storage, stored files show up as shared memory within ~1s.
		if limit == (128+64)<<20 {
			s.mustSucceed(t, small.ID, "head -c 32m /dev/zero > /tmp/fill")
			deadline := time.Now().Add(10 * time.Second)
			for {
				got := strings.TrimSpace(s.mustSucceed(t, small.ID, "awk '/^Shmem:/ {print $2}' /proc/meminfo"))
				if kb, err := strconv.Atoi(got); err == nil && kb >= 32768 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("Shmem = %q kB after writing 32 MiB of storage", got)
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
		// The sandbox user cannot rewrite what it sees.
		if result := s.exec(t, small.ID, "echo x > /proc/meminfo", 10); result.ExitCode == 0 {
			t.Fatal("/proc/meminfo is writable from the sandbox")
		}
	})

	t.Run("limits above the maximum are rejected", func(t *testing.T) {
		status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes", presentation.CreateSandboxRequest{MemoryMB: 1 << 20})
		if status != http.StatusBadRequest || envelope.ErrorCode != "limit_exceeded" {
			t.Fatalf("status %d, %+v", status, envelope)
		}
	})
}
