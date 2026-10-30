package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/model"
)

// SetSnapshotPolicy godoc
// @Summary Configure scheduled and pre-expiry backups
// @Tags snapshots
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Sandbox ID"
// @Param request body model.SnapshotPolicy true "Backup policy; zero interval disables scheduled backups"
// @Success 200 {object} presentation.SandboxEnvelopeDoc
// @Failure 400,404,409,503 {object} presentation.ErrorResponseDoc
// @Router /sandboxes/{id}/snapshot-policy [put]
func (h *Handler) SetSnapshotPolicy(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.SetSnapshotPolicy")
	defer span.End()
	var req model.SnapshotPolicy
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, sandboxEntity, bindingMessage, err)
	}
	result, err := h.service.SetSnapshotPolicy(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, "backup policy saved", result)
}
