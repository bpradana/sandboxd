package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// ListFiles godoc
// @Summary List a directory
// @Description Lists the entries of a directory inside the sandbox, sorted by name. Symlinks are reported, not followed.
// @Tags files
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param path query string true "Absolute directory path inside the sandbox."
// @Success 200 {object} presentation.FileListEnvelopeDoc "Directory entries."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or path not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, or the path is not a directory."
// @Router /api/v1/sandboxes/{id}/files/list [get]
func (h *Handler) ListFiles(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ListFiles")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.FilePathRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, fileEntity, queryMessage, err)
	}

	resp, err := h.service.ListDir(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, fileEntity, err)
	}
	return success(c, http.StatusOK, fileEntity, fileListSuccess, resp)
}
