package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
)

// ListPreviews godoc
// @Summary List a sandbox's open previews
// @Description Reports the previews this daemon currently serves for the sandbox, with the URL and port of each. Previews are state in the daemon that opened them, like the sandbox itself, so anything routing across several daemons rebuilds preview ownership by asking each of them rather than remembering it.
// @Tags previews
// @Produce json
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Success 200 {object} presentation.PreviewListEnvelopeDoc "Open previews."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running (sandbox_not_running)."
// @Router /api/v1/sandboxes/{id}/previews [get]
func (h *Handler) ListPreviews(c fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	span := startSpan(c, "handler.sandbox.ListPreviews")
	defer span.End()

	sessions, err := h.service.ListPreviews(c.Context(), c.Params(paramID))
	if err != nil {
		return serviceError(c, span, "preview", err)
	}
	return success(c, http.StatusOK, "preview", "preview list success", sessions)
}
