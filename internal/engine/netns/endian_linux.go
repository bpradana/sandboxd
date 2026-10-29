//go:build linux

package netns

import "encoding/binary"

func nativePutUint32(buf []byte, value uint32) {
	binary.NativeEndian.PutUint32(buf, value)
}
