package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

func readiness(t *testing.T, env string, engineErr error) (int, readinessResponse) {
	t.Helper()

	eng := engine.NewMockEngine(gomock.NewController(t))
	eng.EXPECT().Ready().Return(engineErr)
	app := fiber.New()
	app.Get("/readyz", readinessHandler(healthParams{
		App:    config.AppConfig{Env: env},
		Build:  buildinfo.Info{Version: "test"},
		Engine: eng,
		Logger: logging.New(zap.NewNop()),
	}))

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var payload readinessResponse
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, payload
}

func TestReadinessReflectsEngine(t *testing.T) {
	t.Parallel()

	status, payload := readiness(t, "development", nil)
	if status != http.StatusOK || payload.Status != "ok" || payload.Checks["engine"] != "ok" {
		t.Fatalf("ready: status=%d payload=%+v", status, payload)
	}

	status, payload = readiness(t, "development", engine.ErrUnsupportedPlatform)
	if status != http.StatusServiceUnavailable || payload.Status != "unhealthy" || payload.Checks["engine"] == "ok" {
		t.Fatalf("unsupported: status=%d payload=%+v", status, payload)
	}
}

func TestReadinessRedactsChecksInProduction(t *testing.T) {
	t.Parallel()

	status, payload := readiness(t, "production", engine.ErrNotReady)
	if status != http.StatusServiceUnavailable || len(payload.Checks) != 0 || payload.Build.Version != "test" {
		t.Fatalf("status=%d payload=%+v", status, payload)
	}
}
