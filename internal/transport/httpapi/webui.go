package httpapi

import (
	"net/http"
	"strings"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/static"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/web"
)

// UIPath is where the web console is served.
const UIPath = "/ui/"

// uiContentSecurityPolicy allows only same-origin scripts and connections
// (including the terminal WebSocket). Inline styles are allowed because the
// terminal emulator injects style elements at runtime.
const uiContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

type webUIParams struct {
	fx.In

	Server *fiber.App
	App    config.AppConfig
}

// registerWebUIRoutes serves the web console. It is static and public, like
// the OpenAPI document; everything it does goes through /api/v1 with the
// key the user enters.
func registerWebUIRoutes(params webUIParams) {
	if !params.App.UIEnabled {
		return
	}

	params.Server.Get("/", func(c fiber.Ctx) error {
		return c.Redirect().Status(http.StatusFound).To(UIPath)
	})
	params.Server.Use(UIPath, func(c fiber.Ctx) error {
		// Routing is not strict, so "/ui" lands here too; its relative asset
		// URLs only resolve under "/ui/".
		if c.Path() == strings.TrimSuffix(UIPath, "/") {
			return c.Redirect().Status(http.StatusFound).To(UIPath)
		}
		c.Set(fiber.HeaderContentSecurityPolicy, uiContentSecurityPolicy)
		c.Set(fiber.HeaderXContentTypeOptions, "nosniff")
		c.Set(fiber.HeaderXFrameOptions, "DENY")
		c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
		return c.Next()
	}, static.New("", static.Config{FS: web.FS()}))
}
