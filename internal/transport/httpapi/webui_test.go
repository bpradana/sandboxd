package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/config"
)

func newWebUIApp(enabled bool) *fiber.App {
	app := fiber.New()
	registerWebUIRoutes(webUIParams{Server: app, App: config.AppConfig{UIEnabled: enabled}})
	return app
}

func TestWebUIServesConsole(t *testing.T) {
	t.Parallel()

	app := newWebUIApp(true)
	res, err := app.Test(httptest.NewRequest(http.MethodGet, UIPath, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "sandboxd console") {
		t.Fatalf("GET %s = %d %q", UIPath, res.StatusCode, body)
	}
	if csp := res.Header.Get(fiber.HeaderContentSecurityPolicy); !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", csp)
	}

	for _, asset := range []string{"app.js", "terminal.js", "vendor/xterm.js", "vendor/addon-fit.js"} {
		res, err := app.Test(httptest.NewRequest(http.MethodGet, UIPath+asset, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s%s = %d", UIPath, asset, res.StatusCode)
		}
	}

	for _, path := range []string{"/", "/ui"} {
		res, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusFound || res.Header.Get(fiber.HeaderLocation) != UIPath {
			t.Errorf("GET %s = %d -> %q", path, res.StatusCode, res.Header.Get(fiber.HeaderLocation))
		}
	}
}

func TestWebUIDisabled(t *testing.T) {
	t.Parallel()

	res, err := newWebUIApp(false).Test(httptest.NewRequest(http.MethodGet, UIPath, nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s with the console disabled = %d", UIPath, res.StatusCode)
	}
}
