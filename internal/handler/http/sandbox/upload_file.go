package sandbox

import (
	"bytes"
	"io"
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// UploadFile godoc
// @Summary Upload a file
// @Description Writes the raw request body to a file inside the sandbox, creating or truncating it. Parent directories must exist. New files are owned by the sandbox user. The body is streamed and capped at SANDBOX_FILE_MAX_BYTES, and also counts against the sandbox's storage_mb.
// @Tags files
// @Accept octet-stream
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param path query string true "Absolute file path inside the sandbox."
// @Param mode query string false "Octal permission for a new file." default(0644)
// @Param body body string true "File content."
// @Success 200 {object} presentation.FileInfoEnvelopeDoc "Written file."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid path or mode."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox or parent directory not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox not running, or the path is a directory."
// @Failure 413 {object} presentation.ErrorResponseDoc "File exceeds SANDBOX_FILE_MAX_BYTES (file_too_large)."
// @Failure 507 {object} presentation.ErrorResponseDoc "Sandbox storage is full (storage_full)."
// @Router /api/v1/sandboxes/{id}/files [put]
func (h *Handler) UploadFile(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.UploadFile")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.WriteFileRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, fileEntity, queryMessage, err)
	}

	body := requestBody(c)
	resp, err := h.service.WriteFile(c.Context(), c.Params(paramID), req, body)
	if err != nil {
		// A rejected upload may still be arriving. Closing on unread data
		// resets the connection, which can destroy the error response before
		// the client reads it; drop a bounded remainder first.
		_, _ = io.Copy(io.Discard, io.LimitReader(body, uploadDrainLimit))
		return serviceError(c, span, fileEntity, err)
	}
	return success(c, http.StatusOK, fileEntity, fileWriteSuccess, resp)
}

// uploadDrainLimit bounds how much of a rejected upload is read and dropped
// so its error response reaches the client; larger remainders are cut off.
const uploadDrainLimit = 64 << 20

// requestBody returns the streamed body when fasthttp is streaming it (large
// uploads under StreamRequestBody), otherwise the buffered bytes.
func requestBody(c fiber.Ctx) io.Reader {
	if stream := c.Request().BodyStream(); stream != nil {
		return stream
	}
	return bytes.NewReader(c.Body())
}
