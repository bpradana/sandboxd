package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// DeleteSession godoc
// @Summary Close a shell session
// @Description Kills the session shell and everything it started.
// @Tags sessions
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param session_id path string true "Session ID."
// @Success 200 {object} presentation.EmptyEnvelopeDoc "Session closed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or session not found."
// @Router /api/v1/sandboxes/{id}/sessions/{session_id} [delete]
func (h *Handler) DeleteSession(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DeleteSession")
	defer span.End(trace.WithStackTrace(true))

	if err := h.service.CloseSession(c.Context(), c.Params(paramID), c.Params(paramSessionID)); err != nil {
		return serviceError(c, span, sessionEntity, err)
	}
	return success(c, http.StatusOK, sessionEntity, sessionDeleteSuccess, nil)
}
