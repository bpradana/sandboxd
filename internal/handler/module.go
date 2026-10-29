// Package handler wires the HTTP handler modules into the application graph.
package handler

import (
	"go.uber.org/fx"

	"github.com/bpradana/sandboxd/internal/handler/http"
)

var Module = fx.Options(
	http.Module,
)
