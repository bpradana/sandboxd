package httpapi

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/docs"
	"github.com/bpradana/sandboxd/internal/config"
)

type docsParams struct {
	fx.In

	Server *fiber.App
	App    config.AppConfig
}

func registerDocsRoutes(params docsParams) {
	if !params.App.DocsEnabled {
		return
	}

	specHandler := func(c fiber.Ctx) error {
		return c.Status(http.StatusOK).Type("json").Send(docs.SwaggerSpec)
	}
	params.Server.Get("/openapi.json", specHandler)
	params.Server.Get("/swagger.json", specHandler)
}
