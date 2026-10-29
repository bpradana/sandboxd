//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runLifecycleCase(t *testing.T) {
	t.Run("labels filter the list", func(t *testing.T) {
		// Unique per run: stopped sandboxes stay listed for the tombstone TTL.
		value := fmt.Sprintf("list-%d", time.Now().UnixNano())
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{Labels: map[string]string{"case": value}})
		status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes?label=case="+value, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		list := decode[[]presentation.SandboxResponse](t, envelope)
		if len(list) != 1 || list[0].ID != sandbox.ID {
			t.Fatalf("list = %+v", list)
		}
	})

	t.Run("delete stops the sandbox and is idempotent", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		for range 2 {
			status, envelope := s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+sandbox.ID, nil)
			requireStatus(t, status, http.StatusOK, envelope)
		}
		got := s.getSandbox(t, sandbox.ID)
		if got.State != "stopped" || got.StopReason != "deleted" || got.StoppedAt == nil {
			t.Fatalf("sandbox = %+v", got)
		}
		status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+sandbox.ID+"/exec", presentation.ExecRequest{Command: "true"})
		if status != http.StatusConflict || envelope.ErrorCode != "sandbox_not_running" {
			t.Fatalf("exec after delete: status %d, %+v", status, envelope)
		}
	})

	t.Run("ttl expiry destroys the sandbox", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{TTLSeconds: 2})
		deadline := time.Now().Add(60 * time.Second)
		for {
			got := s.getSandbox(t, sandbox.ID)
			if got.State == "stopped" {
				if got.StopReason != "expired" {
					t.Fatalf("stop reason = %q", got.StopReason)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("sandbox still %s after its ttl", got.State)
			}
			time.Sleep(time.Second)
		}
	})

	t.Run("running commands keep the sandbox from going idle", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{IdleTimeoutSeconds: 3})
		result := s.exec(t, sandbox.ID, "sleep 8; echo done", 15)
		if result.ExitCode != 0 || result.Stdout != "done\n" {
			t.Fatalf("long command was cut short: %+v", result)
		}
		if got := s.getSandbox(t, sandbox.ID); got.State != "running" {
			t.Fatalf("sandbox %s while a command was running (reason %q)", got.State, got.StopReason)
		}
	})

	t.Run("keepalive extends the expiry", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{TTLSeconds: 60})
		status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+sandbox.ID+"/keepalive", presentation.KeepaliveRequest{TTLSeconds: 600})
		requireStatus(t, status, http.StatusOK, envelope)
		updated := decode[presentation.SandboxResponse](t, envelope)
		if !updated.ExpiresAt.After(sandbox.ExpiresAt.Add(5 * time.Minute)) {
			t.Fatalf("expires_at %v not extended from %v", updated.ExpiresAt, sandbox.ExpiresAt)
		}
	})

	t.Run("unknown sandboxes are 404", func(t *testing.T) {
		status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/sbx-doesnotexist", nil)
		if status != http.StatusNotFound || envelope.ErrorCode != "sandbox_not_found" {
			t.Fatalf("status %d, %+v", status, envelope)
		}
	})
}
