package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// ListImages godoc
// @Summary List base images
// @Description Lists the base root filesystems sandboxes can be created from. Images are imported by the operator with `sandboxdctl image import`.
// @Tags images
// @Produce json
// @Security BearerAuth
// @Success 200 {object} presentation.ImageListEnvelopeDoc "Available images."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 503 {object} presentation.ErrorResponseDoc "Sandbox engine unavailable."
// @Router /api/v1/images [get]
func (h *Handler) ListImages(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ListImages")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.ListImages(c.Context())
	if err != nil {
		return serviceError(c, span, imageEntity, err)
	}
	return success(c, http.StatusOK, imageEntity, imageListSuccess, resp)
}
