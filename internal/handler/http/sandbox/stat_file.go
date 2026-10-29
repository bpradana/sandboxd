package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// StatFile godoc
// @Summary Stat a path
// @Description Returns metadata for a path inside the sandbox without following a final symlink.
// @Tags files
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param path query string true "Absolute path inside the sandbox."
// @Success 200 {object} presentation.FileInfoEnvelopeDoc "Path metadata."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or path not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running."
// @Router /api/v1/sandboxes/{id}/files/stat [get]
func (h *Handler) StatFile(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.StatFile")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.FilePathRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, fileEntity, queryMessage, err)
	}

	resp, err := h.service.StatFile(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, fileEntity, err)
	}
	return success(c, http.StatusOK, fileEntity, fileStatSuccess, resp)
}
