package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// ListSandboxes godoc
// @Summary List sandboxes
// @Description Lists sandboxes held by this server, including recently stopped or failed ones that are still within the tombstone retention window.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Param state query string false "Lifecycle state filter." Enums(creating,running,stopping,stopped,failed)
// @Param label query string false "Label filter as key=value."
// @Success 200 {object} presentation.SandboxListEnvelopeDoc "Sandboxes ordered by creation time."
// @Failure 400 {object} presentation.ErrorResponseDoc "Invalid filter."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Router /api/v1/sandboxes [get]
func (h *Handler) ListSandboxes(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.ListSandboxes")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.ListSandboxesRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, sandboxEntity, queryMessage, err)
	}

	resp, err := h.service.List(c.Context(), req)
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, sandboxListSuccess, resp)
}
