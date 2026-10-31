package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// GetCapacity godoc
// @Summary Report this daemon's capacity
// @Description Reports who this daemon is, whether its engine is usable, how many sandbox slots are free, the limits already promised to running sandboxes, what the machine underneath actually has, and which base images it can start from. A sandbox is kernel state on one host, so anything placing work across several daemons chooses from this rather than from scraped metrics. Promised and actual capacity are reported separately: a daemon can be configured to promise more than its host can deliver, and whether to allow that is the caller's decision.
// @Tags sandboxes
// @Produce json
// @Security BearerAuth
// @Success 200 {object} presentation.CapacityEnvelopeDoc "This daemon's capacity."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Router /api/v1/capacity [get]
func (h *Handler) GetCapacity(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	span := startSpan(c, "handler.sandbox.GetCapacity")
	defer span.End(trace.WithStackTrace(true))

	resp, err := h.service.Capacity(c.Context())
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusOK, sandboxEntity, "capacity success", resp)
}
