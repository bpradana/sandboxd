package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

func newAuthApp(keys ...string) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			return appctx.NewErrorResponseBuilder(err).Build(c)
		},
	})
	app.Use(NewAPIKeyAuthMiddleware(config.AppConfig{APIKeys: keys}, logging.New(zap.NewNop())))
	app.Get("/", func(c fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	return app
}

func TestAPIKeyAuth(t *testing.T) {
	t.Parallel()

	app := newAuthApp("first-key-0123456789", "second-key-0123456789")
	cases := map[string]int{
		"":                                   http.StatusUnauthorized,
		"Bearer":                             http.StatusUnauthorized,
		"Basic Zmlyc3Q=":                     http.StatusUnauthorized,
		"Bearer wrong":                       http.StatusUnauthorized,
		"Bearer first-key-0123456789":        http.StatusNoContent,
		"bearer second-key-0123456789":       http.StatusNoContent,
		"Bearer   second-key-0123456789  ":   http.StatusNoContent,
		"Bearer first-key-0123456789-suffix": http.StatusUnauthorized,
	}
	for header, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			req.Header.Set(fiber.HeaderAuthorization, header)
		}
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("Authorization %q: status %d, want %d", header, res.StatusCode, want)
		}
	}
}

func TestAPIKeyAuthDisabledWithoutKeys(t *testing.T) {
	t.Parallel()

	res, err := newAuthApp().Test(httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestAPIRejectsPreviewOriginEvenWithoutKeys(t *testing.T) {
	t.Parallel()
	app := newAuthApp()
	for _, origin := range []string{"http://preview.localhost:8081", "null"} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
		req.Header.Set("Origin", origin)
		res, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("untrusted origin accepted: %s: %d", origin, res.StatusCode)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "http://localhost:8080/", nil)
	req.Header.Set("Origin", "http://localhost:8080")
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNoContent {
		t.Fatal("same-origin console rejected")
	}
}
