package route

import (
	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/fx"

	sandboxhandler "github.com/bpradana/sandboxd/internal/handler/http/sandbox"
)

type Params struct {
	fx.In

	Server         *fiber.App
	SandboxHandler *sandboxhandler.Handler
	APIKeyAuth     fiber.Handler `name:"api_key_auth"`
	TerminalAuth   fiber.Handler `name:"terminal_auth"`
}

func Register(params Params) {
	// The terminal WebSocket authenticates with a ticket or a bearer key, so
	// it is registered before the API-key group claims the /api/v1 prefix.
	registerTerminalRoute(params, params.Server.Group("/api/v1"))

	api := params.Server.Group("/api/v1", params.APIKeyAuth)

	registerSandboxRoutes(params, api)
}
