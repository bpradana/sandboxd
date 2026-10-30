package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// GetSandboxChanges godoc
// @Summary List what the sandbox changed
// @Description Reports every path the sandbox's workload created, wrote to or removed since it started, read straight out of the overlay's writable layer rather than by comparing the sandbox against its base image. Entries marked seeded are paths the daemon writes into every sandbox (hostname, hosts, resolv.conf, the sandbox account, and the directories mounts go on); hide them when looking for the workload's own work. A sandbox that changed more paths than the daemon will list is reported truncated.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.SandboxChangesEnvelopeDoc "What the workload changed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running (sandbox_not_running)."
// @Router /api/v1/sandboxes/{id}/changes [get]
func (h *Handler) GetSandboxChanges(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	span := startSpan(c, "handler.sandbox.GetSandboxChanges")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.Changes(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, "change list success", resp)
}
