// Package middleware provides the HTTP middleware chain: authentication, panic recovery, request logging, and span error recording.
package middleware

import (
	"go.uber.org/fx"
)

var Module = fx.Options(
	fx.Provide(
		fx.Annotate(
			NewAPIKeyAuthMiddleware,
			fx.ResultTags(`name:"api_key_auth"`),
		),
		fx.Annotate(
			NewTerminalAuthMiddleware,
			fx.ResultTags(`name:"terminal_auth"`),
		),
	),
)
