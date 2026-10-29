package httpapi

import (
	"net/http"
	"strings"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

type healthParams struct {
	fx.In

	App    config.AppConfig
	Build  buildinfo.Info
	Server *fiber.App
	Engine engine.Engine
	Logger *logging.Logger
}

type readinessResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
	Build  buildinfo.Info    `json:"build"`
}

func registerHealthRoutes(params healthParams) {
	params.Server.Get("/healthz", livenessHandler())
	params.Server.Get("/readyz", readinessHandler(params))
}

// Liveness godoc
// @Summary Liveness probe
// @Description Returns 204 when the process is alive.
// @Tags health
// @Success 204
// @Router /healthz [get]
func livenessHandler() fiber.Handler {
	return func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	}
}

// Readiness godoc
// @Summary Readiness probe
// @Description Reports whether the sandbox engine passed its preflight checks (Linux, cgroup v2 delegation, overlayfs, openat2) and can create sandboxes.
// @Description The response includes build info. In environments whose APP_ENV contains "prod" or "staging", checks is returned as an empty object so internal errors are not exposed; the status code (200 or 503) still reflects engine health.
// @Tags health
// @Produce json
// @Success 200 {object} presentation.ReadinessResponseDoc
// @Failure 503 {object} presentation.ReadinessResponseDoc
// @Router /readyz [get]
func readinessHandler(params healthParams) fiber.Handler {
	return func(c fiber.Ctx) error {
		result := map[string]string{"engine": "ok"}
		status := http.StatusOK

		if err := params.Engine.Ready(); err != nil {
			status = http.StatusServiceUnavailable
			result["engine"] = err.Error()
			params.Logger.Ctx(c.Context()).Warn("readiness check failed", zap.String("component", "engine"), zap.Error(err))
		}

		if strings.Contains(params.App.Env, "prod") || strings.Contains(params.App.Env, "staging") {
			result = map[string]string{}
		}

		return c.Status(status).JSON(newReadinessResponse(status, result, params.Build))
	}
}

func newReadinessResponse(status int, checks map[string]string, build buildinfo.Info) readinessResponse {
	responseStatus := "ok"
	if status != http.StatusOK {
		responseStatus = "unhealthy"
	}

	return readinessResponse{
		Status: responseStatus,
		Checks: checks,
		Build:  build,
	}
}
