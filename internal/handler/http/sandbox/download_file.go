package sandbox

import (
	"mime"
	"net/http"
	"path"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// DownloadFile godoc
// @Summary Download a file
// @Description Streams a regular file from the sandbox. The path is resolved inside the sandbox root, so symlinks and `..` can never reach the host filesystem.
// @Tags files
// @Produce octet-stream
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param path query string true "Absolute file path inside the sandbox."
// @Success 200 {file} binary "File content."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or path not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, or the path is a directory."
// @Router /api/v1/sandboxes/{id}/files [get]
func (h *Handler) DownloadFile(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DownloadFile")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.FilePathRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, fileEntity, queryMessage, err)
	}

	reader, info, err := h.service.OpenFile(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, fileEntity, err)
	}

	contentType := mime.TypeByExtension(path.Ext(info.Name))
	if contentType == "" {
		contentType = fiber.MIMEOctetStream
	}
	c.Set(fiber.HeaderContentType, contentType)
	c.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": info.Name}))
	c.Set("X-File-Mode", info.Mode)
	c.Set(fiber.HeaderLastModified, info.ModifiedAt.UTC().Format(http.TimeFormat))
	// fasthttp closes the reader once the body has been written.
	return c.SendStream(reader, int(info.Size))
}
