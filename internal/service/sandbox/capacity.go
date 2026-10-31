package sandbox

import (
	"context"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

// Capacity answers "should the next sandbox be built here?" without the
// caller having to scrape metrics or guess. It reports facts and no policy:
// how much is promised, how much the machine has, and what this host can
// start from.
func (s *service) Capacity(ctx context.Context) (*presentation.CapacityResponse, error) {
	resp := &presentation.CapacityResponse{
		Node:      s.sandbox.NodeID,
		Ready:     s.engine.Ready() == nil,
		Snapshots: s.snapshots != nil,
		Sandboxes: presentation.SandboxSlotsResponse{Max: s.sandbox.MaxSandboxes},
		Images:    []string{},
	}

	sandboxes, err := s.repo.List(ctx, sandboxrepository.Filter{})
	if err != nil {
		return nil, mapError(err)
	}
	for _, sb := range sandboxes {
		// Only a live sandbox holds resources; a stopped one is a record.
		if sb.State == consts.SandboxStateStopped || sb.State == consts.SandboxStateFailed {
			continue
		}
		resp.Sandboxes.Active++
		resp.Allocated.CPUMillis += sb.Limits.CPUMillis
		resp.Allocated.MemoryMB += sb.Limits.MemoryMB
		resp.Allocated.StorageMB += sb.Limits.StorageMB
	}

	// A host whose engine is down still reports its identity and slots, so a
	// placement layer can tell "unusable" apart from "unreachable".
	if host, err := s.engine.HostCapacity(); err == nil {
		resp.Architecture = host.Architecture
		resp.Host = presentation.HostCapacityResponse{
			CPUMillis:         host.CPUMillis,
			MemoryMB:          host.MemoryMB,
			MemoryAvailableMB: host.MemoryAvailableMB,
			StorageTotalMB:    host.StorageTotalMB,
			StorageFreeMB:     host.StorageFreeMB,
		}
	}
	if images, err := s.engine.Images(ctx); err == nil {
		for _, image := range images {
			resp.Images = append(resp.Images, image.Name)
		}
	}
	return resp, nil
}
