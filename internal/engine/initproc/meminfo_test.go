//go:build linux

package initproc

import (
	"strings"
	"testing"
)

func TestFormatMeminfoReflectsTheCgroup(t *testing.T) {
	t.Parallel()

	stat := parseMemoryStat([]byte("anon 10485760\nfile 8388608\nshmem 4194304\nslab_reclaimable 1048576\nunknown 7\n"))
	if stat.Anon != 10<<20 || stat.File != 8<<20 || stat.Shmem != 4<<20 || stat.SlabReclaimable != 1<<20 {
		t.Fatalf("parseMemoryStat() = %+v", stat)
	}

	out := string(formatMeminfo(256<<20, 32<<20, stat))
	for _, want := range []string{
		"MemTotal:               262144 kB\n",
		"MemFree:                229376 kB\n",
		// free + (file - shmem) + reclaimable slab
		"MemAvailable:           234496 kB\n",
		"Shmem:                    4096 kB\n",
		"SwapTotal:                   0 kB\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("meminfo lacks %q:\n%s", want, out)
		}
	}
}

func TestFormatMeminfoHasAFixedSize(t *testing.T) {
	t.Parallel()

	small := formatMeminfo(64<<20, 0, memoryStat{})
	large := formatMeminfo(64<<30, 60<<30, memoryStat{Anon: 50 << 30, File: 9 << 30})
	if len(small) != len(large) {
		t.Fatalf("sizes differ: %d vs %d", len(small), len(large))
	}
	// Usage above the limit (possible briefly) never reports negative free.
	if out := string(formatMeminfo(1<<20, 2<<20, memoryStat{})); !strings.Contains(out, "MemFree:                     0 kB") {
		t.Fatalf("meminfo:\n%s", out)
	}
}
