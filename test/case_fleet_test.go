//go:build e2e

package e2e

import (
	"net/http"
	"runtime"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/preview"
)

// runFleetCase checks what a placement layer reads from a daemon. A sandbox
// is kernel state on one host, so everything here is about being able to tell
// which host, and whether it can take more work.
func (s *e2eSuite) runFleetCase(t *testing.T) {
	capacity := func(t *testing.T) presentation.CapacityResponse {
		t.Helper()
		status, envelope := s.call(t, http.MethodGet, "/api/v1/capacity", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		return decode[presentation.CapacityResponse](t, envelope)
	}

	t.Run("a daemon reports who it is and what the machine has", func(t *testing.T) {
		got := capacity(t)
		if got.Node == "" {
			t.Error("a daemon with no name cannot be routed to")
		}
		if got.Architecture != runtime.GOARCH {
			t.Errorf("architecture = %q, want %q", got.Architecture, runtime.GOARCH)
		}
		if !got.Ready {
			t.Error("the engine is up, so capacity should say so")
		}
		if got.Sandboxes.Max <= 0 {
			t.Errorf("slots = %+v", got.Sandboxes)
		}
		// Read from the kernel, so any sane host has all three.
		if got.Host.CPUMillis <= 0 || got.Host.MemoryMB <= 0 || got.Host.StorageFreeMB <= 0 {
			t.Errorf("host resources were not read: %+v", got.Host)
		}
		if got.Host.MemoryAvailableMB > got.Host.MemoryMB {
			t.Errorf("available memory exceeds total: %+v", got.Host)
		}
		if len(got.Images) == 0 {
			t.Error("a daemon that can build nothing should not be placed on")
		}
	})

	t.Run("a running sandbox is counted against the host", func(t *testing.T) {
		before := capacity(t)
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{CPUMillis: 500, MemoryMB: 128, StorageMB: 64})
		if sandbox.Node != before.Node {
			t.Errorf("sandbox says node %q, daemon says %q", sandbox.Node, before.Node)
		}
		during := capacity(t)
		if during.Sandboxes.Active != before.Sandboxes.Active+1 {
			t.Errorf("active went %d -> %d", before.Sandboxes.Active, during.Sandboxes.Active)
		}
		if during.Allocated.MemoryMB != before.Allocated.MemoryMB+128 || during.Allocated.CPUMillis != before.Allocated.CPUMillis+500 {
			t.Errorf("allocated went %+v -> %+v", before.Allocated, during.Allocated)
		}

		status, envelope := s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+sandbox.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		after := capacity(t)
		if after.Allocated.MemoryMB != before.Allocated.MemoryMB {
			t.Errorf("a destroyed sandbox still holds memory: %+v", after.Allocated)
		}
	})

	t.Run("previews are listed by the daemon that opened them", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		path := "/api/v1/sandboxes/" + sandbox.ID + "/previews"

		status, envelope := s.call(t, http.MethodGet, path, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		if open := decode[[]preview.Session](t, envelope); len(open) != 0 {
			t.Fatalf("a new sandbox has no previews: %+v", open)
		}

		status, envelope = s.call(t, http.MethodPost, path, presentation.CreatePreviewRequest{Port: 3000})
		if status == http.StatusServiceUnavailable {
			t.Skip("previews are disabled on this daemon")
		}
		requireStatus(t, status, http.StatusCreated, envelope)
		opened := decode[preview.Session](t, envelope)

		status, envelope = s.call(t, http.MethodGet, path, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		listed := decode[[]preview.Session](t, envelope)
		if len(listed) != 1 || listed[0].ID != opened.ID || listed[0].Port != 3000 {
			t.Fatalf("the open preview is not listed: %+v", listed)
		}

		status, envelope = s.call(t, http.MethodDelete, path, presentation.RevokePreviewRequest{ID: opened.ID})
		requireStatus(t, status, http.StatusOK, envelope)
		status, envelope = s.call(t, http.MethodGet, path, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		if listed := decode[[]preview.Session](t, envelope); len(listed) != 0 {
			t.Fatalf("a revoked preview is still listed: %+v", listed)
		}
	})
}
