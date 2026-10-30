package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// RemoveImage godoc
// @Summary Remove a base image
// @Description Deletes a base image from this host, and from cloud storage if it was promoted from a snapshot, so other hosts stop resolving the name. Sandboxes already running on the image keep their own mount and are unaffected. Removing a promoted image is also what frees the snapshot underneath it for deletion.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param name path string true "Image name."
// @Success 200 {object} presentation.EmptyEnvelopeDoc "Image removed."
// @Failure 400 {object} presentation.ErrorResponseDoc "Unusable image name."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "No such image here or in cloud storage."
// @Router /api/v1/images/{name} [delete]
func (h *Handler) RemoveImage(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.RemoveImage")
	defer span.End(trace.WithStackTrace(true))

	if err := h.service.RemoveImage(c.Context(), c.Params("name")); err != nil {
		return serviceError(c, span, imageEntity, err)
	}
	return success(c, http.StatusOK, imageEntity, "image remove success", nil)
}
