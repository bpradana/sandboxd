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

func TestWebUIAllowsOnlyConfiguredPreviewFrames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		preview config.PreviewConfig
		source  string
	}{
		{"disabled", config.PreviewConfig{}, "'none'"},
		{"local", config.PreviewConfig{Enabled: true, BaseURL: "http://localhost:18081"}, "http://*.localhost:18081"},
		{"remote", config.PreviewConfig{Enabled: true, BaseURL: "https://preview.example.com"}, "https://*.preview.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app := fiber.New()
			if err := registerWebUIRoutes(webUIParams{Server: app, App: config.AppConfig{UIEnabled: true}, Preview: tc.preview}); err != nil {
				t.Fatal(err)
			}
			res, err := app.Test(httptest.NewRequest(http.MethodGet, UIPath, nil))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = res.Body.Close() }()
			policy := res.Header.Get(fiber.HeaderContentSecurityPolicy)
			if !strings.HasSuffix(policy, "; frame-src "+tc.source) {
				t.Fatalf("preview frame source missing: %s", policy)
			}
			for _, directive := range []string{"script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
				if !strings.Contains(policy, directive) {
					t.Fatalf("console policy weakened: %s", policy)
				}
			}
		})
	}
}

func TestWebUIRejectsInvalidPreviewFrameOrigin(t *testing.T) {
	t.Parallel()
	for _, base := range []string{"http://localhost;script-src:18081", "http://*.localhost:18081", "http://localhost:18081/path", "http://127.0.0.1:18081"} {
		if err := registerWebUIRoutes(webUIParams{Server: fiber.New(), App: config.AppConfig{UIEnabled: true}, Preview: config.PreviewConfig{Enabled: true, BaseURL: base}}); err == nil {
			t.Fatalf("invalid CSP source accepted: %s", base)
		}
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
	for _, name := range []string{"app.js", "cloud-storage.js", "workspace-tools.js", "api.js", "terminal.js", "run.js", "dom.js", "job-progress.js", "file-utils.js", "style.css"} {
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
