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
func registerWebUIRoutes(params webUIParams) error {
	if !params.App.UIEnabled {
		return nil
	}
	console, err := web.LoadConsole()
	if err != nil {
		return err
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
		// HTML and legacy asset URLs must not be reused across daemon builds.
		c.Set(fiber.HeaderCacheControl, "no-store")
		return c.Next()
	})
	index := func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return c.Send(console.Index)
	}
	params.Server.Get(UIPath, index)
	params.Server.Get(UIPath+"index.html", index)
	// Relative module imports stay within this versioned directory, so app.js,
	// cloud-storage.js, terminal.js and their shared api.js always agree.
	params.Server.Use(UIPath+"assets/"+console.Version+"/", static.New("", static.Config{FS: console.FS, MaxAge: 31536000}))
	params.Server.Use(UIPath, static.New("", static.Config{FS: console.FS}))
	return nil
}
