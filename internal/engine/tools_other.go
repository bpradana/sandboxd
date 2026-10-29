//go:build !linux

package engine

import (
	"context"
	"io"

	"github.com/bpradana/sandboxd/internal/config"
)

// Reexec reports whether this process is a re-executed sandbox init or shim.
// Off Linux it never is.
func Reexec() bool {
	return false
}

// ImportImage is Linux-only.
func ImportImage(context.Context, config.SandboxConfig, string, io.Reader) error {
	return ErrUnsupportedPlatform
}

// RemoveImage is Linux-only.
func RemoveImage(config.SandboxConfig, string) error {
	return ErrUnsupportedPlatform
}

// ListImages is Linux-only.
func ListImages(config.SandboxConfig) ([]Image, error) {
	return nil, ErrUnsupportedPlatform
}

// Cleanup is Linux-only.
func Cleanup(context.Context, config.SandboxConfig, config.NetworkConfig) error {
	return ErrUnsupportedPlatform
}
