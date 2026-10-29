// Package route registers the HTTP routes of every domain onto the Fiber application.
package route

import (
	"go.uber.org/fx"

	sandboxservice "github.com/bpradana/sandboxd/internal/service/sandbox"
	"github.com/bpradana/sandboxd/internal/transport/httpapi/middleware"
)

var Module = fx.Options(
	// The terminal auth middleware redeems tickets issued by the sandbox service.
	fx.Provide(func(service sandboxservice.Service) middleware.TicketRedeemer { return service }),
	fx.Invoke(Register),
)
