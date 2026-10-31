package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// PromoteSnapshot godoc
// @Summary Import a snapshot as a base image
// @Description Turns a ready snapshot into a base image that later sandboxes can start from, so prepared work (installed packages, checked-out source) costs nothing per start instead of being installed again every time. The archive is downloaded and extracted the way a restore extracts it: ownership shifted into the sandbox id range, setuid bits stripped, device nodes skipped. The snapshot must have been taken on this architecture, and the image name must be free.
// @Tags snapshots
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Snapshot ID."
// @Param request body presentation.PromoteSnapshotRequest true "Name for the new base image."
// @Success 201 {object} presentation.ImageEnvelopeDoc "The image that was created."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid request body, unusable image name, or a snapshot from another architecture or format."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Snapshot not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Image name already taken, or the snapshot is not ready."
// @Failure 429 {object} presentation.ErrorResponseDoc "Maximum concurrent snapshot operations reached."
// @Failure 503 {object} presentation.ErrorResponseDoc "Filesystem snapshots are disabled."
// @Router /api/v1/snapshots/{id}/image [post]
func (h *Handler) PromoteSnapshot(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.PromoteSnapshot")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.PromoteSnapshotRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, "snapshot", bindingMessage, err)
	}
	resp, err := h.service.PromoteSnapshot(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, "snapshot", err)
	}
	return success(c, http.StatusCreated, "snapshot", "snapshot promoted to image", resp)
}
