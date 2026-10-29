// Package sandbox implements the HTTP handlers for sandbox lifecycle, command execution, sessions, and files.
package sandbox

import (
	"net/http"

	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	sandboxservice "github.com/bpradana/sandboxd/internal/service/sandbox"
)

const (
	sandboxEntity  = "sandbox"
	execEntity     = "exec"
	sessionEntity  = "session"
	fileEntity     = "file"
	imageEntity    = "image"
	terminalEntity = "terminal"

	sandboxCreateSuccess    = "sandbox create success"
	sandboxGetSuccess       = "sandbox get success"
	sandboxListSuccess      = "sandbox list success"
	sandboxStatsSuccess     = "sandbox stats success"
	sandboxKeepaliveSuccess = "sandbox keepalive success"
	sandboxDeleteSuccess    = "sandbox delete success"
	imageListSuccess        = "image list success"
	execSuccess             = "exec success"
	sessionCreateSuccess    = "session create success"
	sessionListSuccess      = "session list success"
	sessionExecSuccess      = "session exec success"
	sessionDeleteSuccess    = "session delete success"
	fileWriteSuccess        = "file write success"
	fileListSuccess         = "file list success"
	fileStatSuccess         = "file stat success"
	fileMkdirSuccess        = "file mkdir success"
	fileDeleteSuccess       = "file delete success"
	terminalTicketSuccess   = "terminal ticket success"

	validationMessage   = "Request validation failed"
	bindingMessage      = "Invalid request body"
	bindingErrorMessage = "request body must be valid JSON"
	queryMessage        = "Invalid query parameters"

	paramID        = "id"
	paramSessionID = "session_id"
)

type Handler struct {
	service sandboxservice.Service
	logger  *logging.Logger
}

func NewHandler(service sandboxservice.Service, logger *logging.Logger) *Handler {
	return &Handler{service: service, logger: logger}
}

// bindError renders a binding or struct-validation failure as a 400.
func bindError(c fiber.Ctx, span trace.Span, entity, message string, err error) error {
	tracing.RecordError(span, err)
	if validation.IsValidationError(err) {
		return appctx.NewResponseBuilder().
			WithCode(http.StatusBadRequest).
			WithEntity(entity).
			WithMessage(validationMessage).
			WithErrors(appctx.ExtractValidationErrors(err)).
			WithErrorCode(appctx.CodeValidation).
			Build(c)
	}
	return appctx.NewResponseBuilder().
		WithCode(http.StatusBadRequest).
		WithEntity(entity).
		WithMessage(message).
		WithError(err.Error()).
		WithErrorCode(appctx.CodeValidation).
		Build(c)
}

// bindBody binds an optional JSON body; an empty body leaves req at its zero
// value. The body is decoded as JSON whatever the Content-Type says: the API
// accepts nothing else, and binding by content type would silently ignore a
// JSON body sent as a form (curl -d without a header), creating a sandbox
// with defaults instead of what was asked for.
func bindBody(c fiber.Ctx, req any) error {
	if len(c.Body()) == 0 {
		return validation.Struct(req)
	}
	if err := c.Bind().JSON(req); err != nil {
		return err
	}
	return validation.Struct(req)
}

// bindQuery binds and validates query parameters.
func bindQuery(c fiber.Ctx, req any) error {
	if err := c.Bind().Query(req); err != nil {
		return err
	}
	return validation.Struct(req)
}

func serviceError(c fiber.Ctx, span trace.Span, entity string, err error) error {
	tracing.RecordError(span, err)
	return appctx.NewErrorResponseBuilder(err).WithEntity(entity).Build(c)
}

func success(c fiber.Ctx, status int, entity, message string, data any) error {
	builder := appctx.NewResponseBuilder().
		WithCode(status).
		WithEntity(entity).
		WithMessage(message)
	if data != nil {
		builder = builder.WithData(data)
	}
	return builder.Build(c)
}

func startSpan(c fiber.Ctx, name string) trace.Span {
	ctx, span := tracing.SpanStart(c.Context(), name)
	c.SetContext(ctx)
	if id := c.Params(paramID); id != "" {
		span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))
	}
	return span
}
