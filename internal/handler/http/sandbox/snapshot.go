package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// CreateSnapshot godoc
// @Summary Save a filesystem snapshot and stop the sandbox
// @Description Stops sandbox processes and asynchronously backs up the complete filesystem to S3-compatible storage. A failed backup retains the source filesystem for retry. Poll the snapshot until ready before restoring. Processes and terminals are not saved.
// @Tags snapshots
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param body body presentation.CreateSnapshotRequest false "Snapshot labels."
// @Success 202 {object} presentation.SnapshotEnvelopeDoc
// @Failure 400 {object} presentation.ErrorResponseDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 404 {object} presentation.ErrorResponseDoc
// @Failure 409 {object} presentation.ErrorResponseDoc
// @Failure 429 {object} presentation.ErrorResponseDoc
// @Failure 503 {object} presentation.ErrorResponseDoc
// @Router /api/v1/sandboxes/{id}/snapshots [post]
func (h *Handler) CreateSnapshot(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.CreateSnapshot")
	defer span.End()
	var req presentation.CreateSnapshotRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, "snapshot", bindingMessage, err)
	}
	resp, err := h.service.CreateSnapshot(c.Context(), c.Params(paramID), req)
	if err != nil {
		return serviceError(c, span, "snapshot", err)
	}
	return success(c, http.StatusAccepted, "snapshot", "snapshot accepted; sandbox will stop", resp)
}

// ListSnapshots godoc
// @Summary List cloud filesystem snapshots
// @Tags snapshots
// @Produce json
// @Security BearerAuth
// @Success 200 {object} presentation.SnapshotListEnvelopeDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 503 {object} presentation.ErrorResponseDoc
// @Router /api/v1/snapshots [get]
func (h *Handler) ListSnapshots(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ListSnapshots")
	defer span.End()
	resp, err := h.service.ListSnapshots(c.Context())
	if err != nil {
		return serviceError(c, span, "snapshot", err)
	}
	return success(c, http.StatusOK, "snapshot", "snapshot list success", resp)
}

// GetSnapshot godoc
// @Summary Get snapshot progress and metadata
// @Tags snapshots
// @Produce json
// @Security BearerAuth
// @Param id path string true "Snapshot ID."
// @Success 200 {object} presentation.SnapshotEnvelopeDoc
// @Failure 400 {object} presentation.ErrorResponseDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 404 {object} presentation.ErrorResponseDoc
// @Failure 503 {object} presentation.ErrorResponseDoc
// @Router /api/v1/snapshots/{id} [get]
func (h *Handler) GetSnapshot(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.GetSnapshot")
	defer span.End()
	resp, err := h.service.GetSnapshot(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, "snapshot", err)
	}
	return success(c, http.StatusOK, "snapshot", "snapshot get success", resp)
}

// DeleteSnapshot godoc
// @Summary Delete a snapshot archive and its cloud manifest
// @Tags snapshots
// @Produce json
// @Security BearerAuth
// @Param id path string true "Snapshot ID."
// @Success 200 {object} presentation.EmptyEnvelopeDoc
// @Failure 400 {object} presentation.ErrorResponseDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 404 {object} presentation.ErrorResponseDoc
// @Failure 409 {object} presentation.ErrorResponseDoc
// @Failure 503 {object} presentation.ErrorResponseDoc
// @Router /api/v1/snapshots/{id} [delete]
func (h *Handler) DeleteSnapshot(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DeleteSnapshot")
	defer span.End()
	if err := h.service.DeleteSnapshot(c.Context(), c.Params(paramID)); err != nil {
		return serviceError(c, span, "snapshot", err)
	}
	return success(c, http.StatusOK, "snapshot", "snapshot delete success", nil)
}
