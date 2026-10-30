package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// CreateSandbox godoc
// @Summary Create a sandbox
// @Description Creates an isolated environment (user, mount, pid, net, uts, ipc and cgroup namespaces; cgroup v2 limits; overlay root on a size-capped tmpfs; seccomp) and waits until it is running. Omitted limits use the server defaults; values above the server maximums are rejected.
// @Description Set snapshot_id instead of image to restore a ready cloud filesystem backup. Saved resource limits, network, environment and labels are inherited unless overridden; TTL starts fresh. Processes and terminal sessions are not restored.
// @Tags sandboxes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body presentation.CreateSandboxRequest false "Sandbox specification."
// @Success 201 {object} presentation.SandboxEnvelopeDoc "Running sandbox."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed, or a limit exceeds the server maximum (limit_exceeded)."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key."
// @Failure 404 {object} presentation.ErrorResponseDoc "Image not found (image_not_found)."
// @Failure 429 {object} presentation.ErrorResponseDoc "Maximum number of sandboxes reached (sandbox_limit_reached)."
// @Failure 500 {object} presentation.ErrorResponseDoc "Sandbox could not be started."
// @Failure 503 {object} presentation.ErrorResponseDoc "Sandbox engine unavailable."
// @Router /api/v1/sandboxes [post]
func (h *Handler) CreateSandbox(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.CreateSandbox")
	defer span.End(trace.WithStackTrace(true))

	var req presentation.CreateSandboxRequest
	if err := bindBody(c, &req); err != nil {
		return bindError(c, span, sandboxEntity, bindingMessage, err)
	}

	resp, err := h.service.Create(c.Context(), req)
	if err != nil {
		return serviceError(c, span, sandboxEntity, err)
	}
	return success(c, http.StatusCreated, sandboxEntity, sandboxCreateSuccess, resp)
}
