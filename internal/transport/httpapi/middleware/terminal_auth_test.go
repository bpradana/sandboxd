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

type fakeTickets map[string]string // ticket -> sandbox id

func (f fakeTickets) RedeemTerminalTicket(sandboxID, ticket string) bool {
	owner, ok := f[ticket]
	delete(f, ticket)
	return ok && owner == sandboxID
}

func newTerminalAuthApp(cfg config.AppConfig, tickets fakeTickets) *fiber.App {
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c fiber.Ctx, err error) error {
			return appctx.NewErrorResponseBuilder(err).Build(c)
		},
	})
	app.Get("/sandboxes/:id/terminal", NewTerminalAuthMiddleware(cfg, logging.New(zap.NewNop()), tickets), func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})
	return app
}

func terminalRequest(t *testing.T, app *fiber.App, target string, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "console.example:8080"
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func TestTerminalAuthWithKeys(t *testing.T) {
	t.Parallel()

	tickets := fakeTickets{"t-a": "sbx-a", "t-b": "sbx-b"}
	app := newTerminalAuthApp(config.AppConfig{APIKeys: []string{"key-0123456789abcdef"}}, tickets)
	sameOrigin := map[string]string{fiber.HeaderOrigin: "http://console.example:8080"}

	if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal", nil); got != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", got)
	}
	if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal", map[string]string{fiber.HeaderAuthorization: "Bearer key-0123456789abcdef"}); got != http.StatusNoContent {
		t.Errorf("bearer key: %d", got)
	}
	if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal?ticket=t-b", sameOrigin); got != http.StatusUnauthorized {
		t.Errorf("ticket for another sandbox: %d", got)
	}
	if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal?ticket=t-a", sameOrigin); got != http.StatusNoContent {
		t.Errorf("valid ticket: %d", got)
	}
	if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal?ticket=t-a", sameOrigin); got != http.StatusUnauthorized {
		t.Errorf("reused ticket: %d", got)
	}
}

func TestTerminalAuthChecksOriginEvenWithoutKeys(t *testing.T) {
	t.Parallel()

	app := newTerminalAuthApp(config.AppConfig{CORSOrigins: []string{"https://allowed.example"}}, fakeTickets{})
	cases := map[string]int{
		"":                            http.StatusNoContent, // non-browser client
		"http://console.example:8080": http.StatusNoContent, // same origin
		"https://allowed.example":     http.StatusNoContent,
		"https://evil.example":        http.StatusForbidden,
		"http://console.example:9999": http.StatusForbidden,
		"null":                        http.StatusForbidden,
	}
	for origin, want := range cases {
		headers := map[string]string{}
		if origin != "" {
			headers[fiber.HeaderOrigin] = origin
		}
		if got := terminalRequest(t, app, "/sandboxes/sbx-a/terminal", headers); got != want {
			t.Errorf("Origin %q: status %d, want %d", origin, got, want)
		}
	}
}

func TestRedactURI(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"/api/v1/sandboxes":                               "/api/v1/sandboxes",
		"/api/v1/sandboxes?state=running":                 "/api/v1/sandboxes?state=running",
		"/api/v1/sandboxes/x/terminal?ticket=abc&cols=80": "/api/v1/sandboxes/x/terminal?cols=80&ticket=REDACTED",
	}
	for in, want := range cases {
		if got := redactURI(in); got != want {
			t.Errorf("redactURI(%q) = %q, want %q", in, got, want)
		}
	}
}
