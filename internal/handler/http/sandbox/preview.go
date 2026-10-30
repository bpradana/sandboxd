package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// CreatePreview godoc
// @Summary Open an isolated HTTP and WebSocket app preview
// @Tags previews
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Sandbox ID"
// @Param request body presentation.CreatePreviewRequest true "HTTP port inside the sandbox"
// @Success 201 {object} presentation.PreviewEnvelopeDoc
// @Failure 400,404,409,429,503 {object} presentation.ErrorResponseDoc
// @Router /sandboxes/{id}/previews [post]
func (h *Handler) CreatePreview(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	span := startSpan(c, "handler.sandbox.CreatePreview")
	defer span.End()
	var req presentation.CreatePreviewRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, "preview", bindingMessage, err)
	}
	result, err := h.service.CreatePreview(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, "preview", err)
	}
	return success(c, http.StatusCreated, "preview", "preview opened", result)
}

// RevokePreview godoc
// @Summary Close an app preview
// @Tags previews
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Sandbox ID"
// @Param request body presentation.RevokePreviewRequest true "Preview session"
// @Success 200 {object} presentation.ErrorResponseDoc
// @Failure 400,404 {object} presentation.ErrorResponseDoc
// @Router /sandboxes/{id}/previews [delete]
func (h *Handler) RevokePreview(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	span := startSpan(c, "handler.sandbox.RevokePreview")
	defer span.End()
	var req presentation.RevokePreviewRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, "preview", bindingMessage, err)
	}
	if err := h.service.RevokePreview(c.Context(), c.Params(paramID), req); err != nil {
		return serviceError(c, span, "preview", err)
	}
	return success(c, http.StatusOK, "preview", "preview closed", nil)
}
