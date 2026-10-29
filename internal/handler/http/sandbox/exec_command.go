package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// ExecCommand godoc
// @Summary Run a command
// @Description Runs one command in the sandbox as the unprivileged sandbox user and waits for it to finish. `command` runs through /bin/sh -c; `argv` runs directly. On timeout the command's whole process group is killed and timed_out is true. stdout and stderr are each capped at SANDBOX_EXEC_MAX_OUTPUT_BYTES; excess output is dropped and flagged as truncated.
// @Tags exec
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.ExecRequest true "Command to run."
// @Success 200 {object} presentation.ExecEnvelopeDoc "Command finished (any exit code)."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed, or timeout above the server maximum."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Failure 429 {object} presentation.ErrorResponseDoc "Too many concurrent commands in this sandbox (exec_limit_reached)."
// @Router /api/v1/sandboxes/{id}/exec [post]
func (h *Handler) ExecCommand(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ExecCommand")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.ExecRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, execEntity, bindingMessage, err)
	}

	resp, err := h.service.Exec(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, execEntity, err)
	}
	return success(c, http.StatusOK, execEntity, execSuccess, resp)
}
