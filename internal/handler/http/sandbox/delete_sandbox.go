package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// DeleteSandbox godoc
// @Summary Destroy a sandbox
// @Description Kills every process in the sandbox and releases its cgroup, mounts, storage, and network. Idempotent for sandboxes that are already stopped.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.EmptyEnvelopeDoc "Sandbox destroyed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 500 {object} presentation.ErrorResponseDoc "Teardown failed; the sandbox is marked failed."
// @Router /api/v1/sandboxes/{id} [delete]
func (h *Handler) DeleteSandbox(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DeleteSandbox")
	defer span.End(trace.WithStackTrace(true))

	if err := h.service.Destroy(c.Context(), c.Params(paramID)); err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, sandboxDeleteSuccess, nil)
}
