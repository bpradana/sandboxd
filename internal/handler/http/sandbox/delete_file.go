package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// DeleteFile godoc
// @Summary Delete a path
// @Description Removes a file, symlink, or empty directory; recursive=true removes non-empty directories. The sandbox root cannot be removed.
// @Tags files
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param path query string true "Absolute path inside the sandbox."
// @Param recursive query bool false "Remove directories recursively."
// @Success 200 {object} presentation.EmptyEnvelopeDoc "Path removed."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or path not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, or the directory is not empty."
// @Router /api/v1/sandboxes/{id}/files [delete]
func (h *Handler) DeleteFile(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DeleteFile")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.RemoveFileRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, fileEntity, queryMessage, err)
	}

	if err := h.service.RemoveFile(c.Context(), c.Params(paramID), req); err != nil {
		return serviceError(c, span, fileEntity, err)
	}
	return success(c, http.StatusOK, fileEntity, fileDeleteSuccess, nil)
}
