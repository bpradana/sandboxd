package sandbox

import (
	"mime"
	"net/http"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/presentation"
)

// GetCloudStorage godoc
// @Summary Get cloud storage connection settings without credentials
// @Tags snapshots
// @Produce json
// @Security BearerAuth
// @Success 200 {object} presentation.CloudStorageEnvelopeDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Router /api/v1/settings/cloud-storage [get]
func (h *Handler) GetCloudStorage(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.GetCloudStorage")
	defer span.End()
	c.Set(fiber.HeaderCacheControl, "no-store")
	resp, err := h.service.GetCloudStorage(c.Context())
	if err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	return success(c, http.StatusOK, "cloud_storage", "cloud storage settings", resp)
}

// bindCloudStorage never echoes rejected values, since validation errors for
// credential fields must not leak into responses, logs or spans. Requiring JSON
// also prevents cross-site simple form POSTs in unauthenticated development.
func bindCloudStorage(c fiber.Ctx, req *presentation.CloudStorageRequest) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	mediaType, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	if err != nil || mediaType != fiber.MIMEApplicationJSON {
		return appctx.NewHTTPError(http.StatusUnsupportedMediaType, "cloud storage settings require application/json").WithErrorCode(appctx.CodeValidation)
	}
	if err := bindBody(c, req); err != nil {
		return appctx.NewHTTPError(http.StatusBadRequest, "invalid cloud storage settings").WithErrorCode(appctx.CodeValidation)
	}
	return nil
}

// TestCloudStorage godoc
// @Summary Test cloud list, write, read and delete permissions without saving
// @Tags snapshots
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body presentation.CloudStorageRequest true "Connection details. Credentials are never returned."
// @Success 200 {object} presentation.EmptyEnvelopeDoc
// @Failure 400 {object} presentation.ErrorResponseDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 415 {object} presentation.ErrorResponseDoc
// @Failure 502 {object} presentation.ErrorResponseDoc
// @Router /api/v1/settings/cloud-storage/test [post]
func (h *Handler) TestCloudStorage(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.TestCloudStorage")
	defer span.End()
	var req presentation.CloudStorageRequest
	if err := bindCloudStorage(c, &req); err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	if err := h.service.TestCloudStorage(c.Context(), req); err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	return success(c, http.StatusOK, "cloud_storage", "cloud storage connection test passed", nil)
}

// SaveCloudStorage godoc
// @Summary Test and save an encrypted deployment-wide cloud storage connection
// @Description Takes effect immediately and survives daemon restarts. Refused while snapshot operations use the current connection. Saved settings take precedence over environment configuration.
// @Tags snapshots
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body presentation.CloudStorageRequest true "Connection details. Credentials are never returned."
// @Success 200 {object} presentation.CloudStorageEnvelopeDoc
// @Failure 400 {object} presentation.ErrorResponseDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 409 {object} presentation.ErrorResponseDoc
// @Failure 415 {object} presentation.ErrorResponseDoc
// @Failure 502 {object} presentation.ErrorResponseDoc
// @Router /api/v1/settings/cloud-storage [put]
func (h *Handler) SaveCloudStorage(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.SaveCloudStorage")
	defer span.End()
	var req presentation.CloudStorageRequest
	if err := bindCloudStorage(c, &req); err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	resp, err := h.service.SaveCloudStorage(c.Context(), req)
	if err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	return success(c, http.StatusOK, "cloud_storage", "cloud storage connected", resp)
}

// DisconnectCloudStorage godoc
// @Summary Disconnect cloud storage and forget saved credentials
// @Description Does not delete cloud backups. Persists across restarts, including when environment-based snapshot configuration exists. Refused while snapshot operations use the connection.
// @Tags snapshots
// @Produce json
// @Security BearerAuth
// @Success 200 {object} presentation.EmptyEnvelopeDoc
// @Failure 401 {object} presentation.ErrorResponseDoc
// @Failure 409 {object} presentation.ErrorResponseDoc
// @Router /api/v1/settings/cloud-storage [delete]
func (h *Handler) DisconnectCloudStorage(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.DisconnectCloudStorage")
	defer span.End()
	c.Set(fiber.HeaderCacheControl, "no-store")
	if err := h.service.DisconnectCloudStorage(c.Context()); err != nil {
		return serviceError(c, span, "cloud_storage", err)
	}
	return success(c, http.StatusOK, "cloud_storage", "cloud storage disconnected; backups retained", nil)
}
