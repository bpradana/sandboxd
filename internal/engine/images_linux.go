//go:build linux

package engine

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/bpradana/sandboxd/internal/engine/image"
)

// ImportImage turns an archive into a base image future sandboxes can start
// from. It is the same extraction a snapshot restore does into its private
// lower layer, committed under a name instead: ownership shifted into the
// sandbox id range, setuid bits stripped, device nodes skipped, and every
// path confined to the image being built.
func (e *linuxEngine) ImportImage(ctx context.Context, name string, src io.Reader, maxBytes int64) (*Image, error) {
	if err := e.Ready(); err != nil {
		return nil, err
	}
	if !image.ValidName(name) {
		return nil, ErrInvalidImageName
	}
	if err := e.images.ImportSnapshot(ctx, name, src, maxBytes); err != nil {
		switch {
		case errors.Is(err, image.ErrExists):
			return nil, ErrImageExists
		case errors.Is(err, image.ErrInvalidName):
			return nil, ErrInvalidImageName
		}
		return nil, err
	}
	dir, err := e.images.Path(name)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	return &Image{Name: name, CreatedAt: info.ModTime()}, nil
}
