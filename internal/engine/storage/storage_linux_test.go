//go:build linux

package storage

import (
	"testing"
	"unsafe"
)

// LOOP_CONFIGURE reads these layouts; a size change would make the kernel
// read the wrong bytes.
func TestLoopStructSizes(t *testing.T) {
	t.Parallel()

	if got := unsafe.Sizeof(loopInfo64{}); got != 232 {
		t.Errorf("sizeof(loop_info64) = %d, want 232", got)
	}
	if got := unsafe.Sizeof(loopConfig{}); got != 304 {
		t.Errorf("sizeof(loop_config) = %d, want 304", got)
	}
}

func TestDirtyLimitIsAQuarterOfMemoryWithinBounds(t *testing.T) {
	t.Parallel()

	cases := map[int64]int64{
		16 << 20:  4 << 20,  // floor
		64 << 20:  16 << 20, // a quarter
		512 << 20: 64 << 20, // ceiling
		8 << 30:   64 << 20, // ceiling
	}
	for memory, want := range cases {
		if got := DirtyLimit(memory); got != want {
			t.Errorf("DirtyLimit(%d) = %d, want %d", memory, got, want)
		}
	}
}
