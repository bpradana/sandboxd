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
