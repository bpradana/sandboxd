package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// CreateTerminalTicket godoc
// @Summary Issue a terminal ticket
// @Description Returns a single-use ticket, valid for 30 seconds, that authenticates one WebSocket upgrade on GET /api/v1/sandboxes/{id}/terminal. Browsers need it because they cannot send an Authorization header on a WebSocket.
// @Tags terminal
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 201 {object} presentation.TerminalTicketEnvelopeDoc "Ticket issued."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Router /api/v1/sandboxes/{id}/terminal/tickets [post]
func (h *Handler) CreateTerminalTicket(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.CreateTerminalTicket")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.CreateTerminalTicket(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, terminalEntity, err)
	}
	return success(c, http.StatusCreated, terminalEntity, terminalTicketSuccess, resp)
}
