package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// CreateSession godoc
// @Summary Open a shell session
// @Description Starts a long-lived shell inside the sandbox. Commands sent to the session share shell state -- working directory, exported variables, activated virtualenvs -- across calls.
// @Tags sessions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.CreateSessionRequest false "Session options."
// @Success 201 {object} presentation.SessionEnvelopeDoc "Session opened."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed, or the session limit is reached."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Router /api/v1/sandboxes/{id}/sessions [post]
func (h *Handler) CreateSession(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.CreateSession")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.CreateSessionRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, sessionEntity, bindingMessage, err)
	}

	resp, err := h.service.CreateSession(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, sessionEntity, err)
	}
	return success(c, http.StatusCreated, sessionEntity, sessionCreateSuccess, resp)
}
