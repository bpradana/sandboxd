package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// ListSessions godoc
// @Summary List shell sessions
// @Tags sessions
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.SessionListEnvelopeDoc "Sessions ordered by creation time."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Router /api/v1/sandboxes/{id}/sessions [get]
func (h *Handler) ListSessions(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ListSessions")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.ListSessions(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, sessionEntity, err)
	}
	return success(c, http.StatusOK, sessionEntity, sessionListSuccess, resp)
}
