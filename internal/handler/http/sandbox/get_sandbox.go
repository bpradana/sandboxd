package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// GetSandbox godoc
// @Summary Get a sandbox
// @Description Returns the sandbox state, limits, and timestamps. Running sandboxes also include live resource statistics.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.SandboxEnvelopeDoc "Sandbox."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found (sandbox_not_found)."
// @Router /api/v1/sandboxes/{id} [get]
func (h *Handler) GetSandbox(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.GetSandbox")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.Get(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, sandboxGetSuccess, resp)
}
