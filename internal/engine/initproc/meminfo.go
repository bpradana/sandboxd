//go:build linux

package initproc

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
)

// memoryStat holds the memory.stat fields /proc/meminfo is derived from.
type memoryStat struct {
	Anon, File, Shmem, FileMapped            int64
	ActiveAnon, InactiveAnon                 int64
	ActiveFile, InactiveFile                 int64
	Slab, SlabReclaimable, SlabUnreclaimable int64
	KernelStack, PageTables                  int64
}

func parseMemoryStat(data []byte) memoryStat {
	var stat memoryStat
	fields := map[string]*int64{
		"anon": &stat.Anon, "file": &stat.File, "shmem": &stat.Shmem, "file_mapped": &stat.FileMapped,
		"active_anon": &stat.ActiveAnon, "inactive_anon": &stat.InactiveAnon,
		"active_file": &stat.ActiveFile, "inactive_file": &stat.InactiveFile,
		"slab": &stat.Slab, "slab_reclaimable": &stat.SlabReclaimable, "slab_unreclaimable": &stat.SlabUnreclaimable,
		"kernel_stack": &stat.KernelStack, "pagetables": &stat.PageTables,
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		key, value, ok := bytes.Cut(scanner.Bytes(), []byte(" "))
		if !ok {
			continue
		}
		if field, known := fields[string(key)]; known {
			*field, _ = strconv.ParseInt(string(value), 10, 64)
		}
	}
	return stat
}

// meminfoLine is a fixed-width /proc/meminfo line. The width never varies,
// so the file can be rewritten in place without changing its size and a
// concurrent reader never sees it truncated.
const meminfoLine = "%-16s%14d kB\n"

// formatMeminfo renders /proc/meminfo for a cgroup: the cgroup limit is the
// machine's memory, and usage comes from the cgroup instead of the host.
// There is no swap (memory.swap.max is 0).
func formatMeminfo(limit, current int64, stat memoryStat) []byte {
	if current > limit {
		current = limit
	}
	free := limit - current
	// Like the kernel's estimate: free memory plus page cache and slab that
	// can be reclaimed. tmpfs pages (shmem) cannot be, and are the sandbox's
	// storage.
	available := free + max(stat.File-stat.Shmem, 0) + stat.SlabReclaimable
	available = min(available, limit)

	kb := func(bytes int64) int64 { return bytes / 1024 }
	lines := []struct {
		name  string
		bytes int64
	}{
		{"MemTotal:", limit},
		{"MemFree:", free},
		{"MemAvailable:", available},
		{"Buffers:", 0},
		{"Cached:", stat.File},
		{"SwapCached:", 0},
		{"Active:", stat.ActiveAnon + stat.ActiveFile},
		{"Inactive:", stat.InactiveAnon + stat.InactiveFile},
		{"Active(anon):", stat.ActiveAnon},
		{"Inactive(anon):", stat.InactiveAnon},
		{"Active(file):", stat.ActiveFile},
		{"Inactive(file):", stat.InactiveFile},
		{"SwapTotal:", 0},
		{"SwapFree:", 0},
		{"AnonPages:", stat.Anon},
		{"Mapped:", stat.FileMapped},
		{"Shmem:", stat.Shmem},
		{"Slab:", stat.Slab},
		{"SReclaimable:", stat.SlabReclaimable},
		{"SUnreclaim:", stat.SlabUnreclaimable},
		{"KernelStack:", stat.KernelStack},
		{"PageTables:", stat.PageTables},
	}
	var buf bytes.Buffer
	for _, line := range lines {
		fmt.Fprintf(&buf, meminfoLine, line.name, kb(line.bytes))
	}
	return buf.Bytes()
}

func parseInt(data []byte) (int64, error) {
	return strconv.ParseInt(string(bytes.TrimSpace(data)), 10, 64)
}
