package sandbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// ExecCommandStream godoc
// @Summary Run a command and stream its output
// @Description Same request as POST /exec, but responds with Server-Sent Events as output arrives: `stdout` and `stderr` events carry {"data": "..."}; the final `exit` event carries {"result": {...}} (stdout/stderr omitted), or an `error` event carries {"error": "..."}. Validation and lookup failures before the command starts are returned as regular JSON errors. Disconnecting kills the command.
// @Tags exec
// @Accept json
// @Produce text/event-stream
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.ExecRequest true "Command to run."
// @Success 200 {object} presentation.ExecStreamEvent "Event stream; each data line is one ExecStreamEvent."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Router /api/v1/sandboxes/{id}/exec/stream [post]
func (h *Handler) ExecCommandStream(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ExecCommandStream")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.ExecRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, execEntity, bindingMessage, err)
	}
	// Fail fast with a JSON error while headers can still change.
	if _, err := h.service.Stats(c.Context(), c.Params(paramID)); err != nil {
		return serviceError(c, span, execEntity, err)
	}

	id := c.Params(paramID)
	// The stream writer runs after this handler returns, so it must not use
	// the request-scoped fiber.Ctx; the command's own timeout bounds it.
	ctx := context.WithoutCancel(c.Context())

	c.Set(fiber.HeaderContentType, "text/event-stream")
	c.Set(fiber.HeaderCacheControl, "no-cache")
	c.Set(fiber.HeaderConnection, "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	return c.SendStreamWriter(func(w *bufio.Writer) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		emit := func(event string, payload presentation.ExecStreamEvent) error {
			body, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body); err != nil {
				cancel()
				return err
			}
			if err := w.Flush(); err != nil {
				// Client went away: returning the error kills the command.
				cancel()
				return err
			}
			return nil
		}

		if err := h.service.ExecStream(streamCtx, id, req, emit); err != nil {
			h.logger.Ctx(ctx).Warn("exec stream ended with error", zap.String("sandbox_id", id), zap.Error(err))
		}
	})
}
