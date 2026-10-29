//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

func (s *e2eSuite) runHealthCase(t *testing.T) {
	t.Run("healthz is public", func(t *testing.T) {
		status, _ := s.raw(t, http.MethodGet, "/healthz", nil, "")
		if status != http.StatusNoContent {
			t.Fatalf("status = %d", status)
		}
	})

	t.Run("readyz reports the engine", func(t *testing.T) {
		status, _ := s.raw(t, http.MethodGet, "/readyz", nil, "")
		if status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
	})

	t.Run("an image is available", func(t *testing.T) {
		status, envelope := s.call(t, http.MethodGet, "/api/v1/images", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		if string(envelope.Data) == "[]" {
			t.Fatal("no images imported; run `mise run docker image`")
		}
	})
}

func (s *e2eSuite) runAuthCase(t *testing.T) {
	if s.apiKey == "" {
		t.Skipf("%s is not set; the server runs without authentication", envAPIKey)
	}
	status, _ := s.raw(t, http.MethodGet, "/api/v1/sandboxes", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("missing key: status %d", status)
	}
	status, _ = s.raw(t, http.MethodGet, "/api/v1/sandboxes", nil, "wrong-key")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong key: status %d", status)
	}
}
