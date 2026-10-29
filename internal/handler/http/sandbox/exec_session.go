package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
)

// ExecSession godoc
// @Summary Run a command in a shell session
// @Description Writes the command to the session shell and waits for it to finish. A session runs one command at a time. If the command times out the session is terminated, because its shell state is no longer known.
// @Tags sessions
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param session_id path string true "Session ID."
// @Param body body presentation.SessionExecRequest true "Command to run."
// @Success 200 {object} presentation.ExecEnvelopeDoc "Command finished."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or session not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, session busy (session_busy), or session closed (session_closed)."
// @Router /api/v1/sandboxes/{id}/sessions/{session_id}/exec [post]
func (h *Handler) ExecSession(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ExecSession")
	defer span.End(trace.WithStackTrace(true))
	span.SetAttributes(tracing.String(tracing.SessionIDKey, c.Params(paramSessionID)))

	var req presentation.SessionExecRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, sessionEntity, bindingMessage, err)
	}

	resp, err := h.service.SessionExec(c.Context(), c.Params(paramID), c.Params(paramSessionID), req)
	if err != nil {
		return serviceError(c, span, sessionEntity, err)
	}
	return success(c, http.StatusOK, sessionEntity, sessionExecSuccess, resp)
}
