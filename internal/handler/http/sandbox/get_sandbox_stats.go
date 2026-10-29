package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// GetSandboxStats godoc
// @Summary Get sandbox resource statistics
// @Description Returns cgroup memory, CPU, process, and OOM counters plus writable-layer usage.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.SandboxStatsEnvelopeDoc "Resource statistics."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running (sandbox_not_running)."
// @Router /api/v1/sandboxes/{id}/stats [get]
func (h *Handler) GetSandboxStats(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.GetSandboxStats")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.Stats(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, sandboxStatsSuccess, resp)
}
