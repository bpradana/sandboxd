package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// KeepaliveSandbox godoc
// @Summary Extend a sandbox's lifetime
// @Description Resets the expiry to now + ttl_seconds (default SANDBOX_DEFAULT_TTL), never beyond SANDBOX_MAX_TTL after creation, and counts as activity for the idle timeout.
// @Tags sandboxes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.KeepaliveRequest false "New lifetime."
// @Success 200 {object} presentation.SandboxEnvelopeDoc "Updated sandbox."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Router /api/v1/sandboxes/{id}/keepalive [post]
func (h *Handler) KeepaliveSandbox(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.KeepaliveSandbox")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.KeepaliveRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, sandboxEntity, bindingMessage, err)
	}

	resp, err := h.service.Keepalive(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, sandboxKeepaliveSuccess, resp)
}
