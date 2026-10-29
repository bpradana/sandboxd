package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// MakeDirectory godoc
// @Summary Create a directory
// @Description Creates a directory owned by the sandbox user; with parents=true missing parents are created too and an existing directory is not an error.
// @Tags files
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.MkdirRequest true "Directory to create."
// @Success 201 {object} presentation.FileInfoEnvelopeDoc "Created directory."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path or mode."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or parent not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, or the path already exists."
// @Router /api/v1/sandboxes/{id}/files/mkdir [post]
func (h *Handler) MakeDirectory(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.MakeDirectory")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.MkdirRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, fileEntity, bindingMessage, err)
	}

	resp, err := h.service.Mkdir(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, fileEntity, err)
	}
	return success(c, http.StatusCreated, fileEntity, fileMkdirSuccess, resp)
}
