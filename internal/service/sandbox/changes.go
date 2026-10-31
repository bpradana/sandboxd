package sandbox

import (
	"context"

	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/presentation"
)

// Changes reports what the workload did to the filesystem it was given. The
// engine reads it out of the overlay's writable layer, so this costs one walk
// of what actually changed rather than a comparison against the base image.
func (s *service) Changes(ctx context.Context, id string) (*presentation.SandboxChangesResponse, error) {
	if _, err := s.runningSandbox(ctx, id); err != nil {
		return nil, err
	}
	set, err := s.engine.Changes(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	entries := make([]presentation.SandboxChangeResponse, 0, len(set.Entries))
	for _, change := range set.Entries {
		entry := presentation.SandboxChangeResponse{
			Path:   change.Path,
			Kind:   change.Kind,
			Type:   change.Type,
			Size:   change.Size,
			Seeded: change.Seeded,
		}
		// A deletion leaves nothing behind to describe.
		if change.Kind != engine.ChangeDeleted {
			entry.Mode = formatMode(change.Mode)
			modified := change.ModTime
			entry.ModifiedAt = &modified
		}
		entries = append(entries, entry)
	}
	return &presentation.SandboxChangesResponse{Entries: entries, Truncated: set.Truncated}, nil
}
