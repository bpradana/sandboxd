package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/config"
)

func newWebUIApp(t *testing.T, enabled bool) *fiber.App {
	t.Helper()
	app := fiber.New()
	if err := registerWebUIRoutes(webUIParams{Server: app, App: config.AppConfig{UIEnabled: enabled}}); err != nil {
		t.Fatal(err)
	}
	return app
}

func TestWebUIServesConsole(t *testing.T) {
	t.Parallel()

	app := newWebUIApp(t, true)
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

	res, err := newWebUIApp(t, false).Test(httptest.NewRequest(http.MethodGet, UIPath, nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET %s with the console disabled = %d", UIPath, res.StatusCode)
	}
}

func TestWebUIKeepsSettingsAssetsInOneVersion(t *testing.T) {
	t.Parallel()
	app := newWebUIApp(t, true)
	res, err := app.Test(httptest.NewRequest(http.MethodGet, UIPath, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.Header.Get(fiber.HeaderCacheControl) != "no-store" {
		t.Fatal("HTML can reuse old assets")
	}
	match := regexp.MustCompile(`assets/([a-f0-9]{64})/app.js`).FindStringSubmatch(string(body))
	if len(match) != 2 {
		t.Fatal("HTML does not load a versioned app module")
	}
	prefix := UIPath + "assets/" + match[1] + "/"
	if !strings.Contains(string(body), `href="assets/`+match[1]+`/style.css"`) {
		t.Fatal("HTML and CSS have different asset versions")
	}
	for _, name := range []string{"app.js", "cloud-storage.js", "api.js", "terminal.js", "style.css"} {
		res, err := app.Test(httptest.NewRequest(http.MethodGet, prefix+name, nil))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK || len(data) == 0 {
			t.Fatalf("missing settings dependency %s: %d", name, res.StatusCode)
		}
		if !strings.Contains(res.Header.Get(fiber.HeaderCacheControl), "max-age=31536000") {
			t.Fatalf("versioned asset cache %s", name)
		}
		if name == "cloud-storage.js" && !strings.Contains(string(data), `$("#cloud-storage-open").addEventListener("click"`) {
			t.Fatal("cloud tab handler is missing")
		}
	}
}
