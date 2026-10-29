// Package helper holds small, dependency-free utilities shared across layers.
package helper

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID returns prefix + "-" + 24 random hex characters. IDs double as cgroup
// and directory names, so they stay within [a-z0-9-].
func NewID(prefix string) string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
