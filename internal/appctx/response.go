package appctx

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	fiber "github.com/gofiber/fiber/v3"

	"github.com/bpradana/sandboxd/internal/helper/validation"
)

// Response defines the canonical API response payload.
type Response struct {
	Code      int            `json:"code"`
	Message   string         `json:"message"`
	Entity    string         `json:"entity"`
	Data      any            `json:"data"`
	Error     string         `json:"error,omitempty"`
	Errors    any            `json:"errors,omitempty"`
	Meta      any            `json:"meta,omitempty"`
	ErrorCode string         `json:"error_code,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// ResponseBuilder provides a fluent interface for constructing responses.
type ResponseBuilder struct {
	response *Response
	source   error
}

type responseErrorContextKey struct{}

// NewResponseBuilder initializes a ResponseBuilder with default values.
func NewResponseBuilder() *ResponseBuilder {
	return &ResponseBuilder{
		response: &Response{
			Data: map[string]any{},
		},
	}
}

func (rb *ResponseBuilder) ensureDefaults() {
	if rb.response.Data == nil {
		rb.response.Data = map[string]any{}
	}
}

// WithCode sets the response code field.
func (rb *ResponseBuilder) WithCode(code int) *ResponseBuilder {
	rb.response.Code = code
	return rb
}

// WithMessage sets the response message.
func (rb *ResponseBuilder) WithMessage(message string) *ResponseBuilder {
	rb.response.Message = message
	return rb
}

// WithEntity sets the logical resource/entity name.
func (rb *ResponseBuilder) WithEntity(entity string) *ResponseBuilder {
	rb.response.Entity = entity
	return rb
}

// WithData attaches the payload data.
func (rb *ResponseBuilder) WithData(data any) *ResponseBuilder {
	rb.response.Data = data
	return rb
}

// WithError assigns a primary error string.
func (rb *ResponseBuilder) WithError(errMsg string) *ResponseBuilder {
	rb.response.Error = strings.TrimSpace(errMsg)
	return rb
}

// WithErrors attaches a list of validation or domain errors.
func (rb *ResponseBuilder) WithErrors(errors any) *ResponseBuilder {
	if isEmptyErrors(errors) {
		return rb
	}
	rb.response.Errors = errors
	return rb
}

// WithMeta sets optional metadata.
func (rb *ResponseBuilder) WithMeta(meta any) *ResponseBuilder {
	rb.response.Meta = meta
	return rb
}

// WithErrorCode sets a machine-readable error code.
func (rb *ResponseBuilder) WithErrorCode(code string) *ResponseBuilder {
	code = strings.TrimSpace(code)
	if code == "" {
		return rb
	}
	rb.response.ErrorCode = code
	return rb
}

// WithDetail adds a contextual detail entry.
func (rb *ResponseBuilder) WithDetail(key string, value any) *ResponseBuilder {
	key = strings.TrimSpace(key)
	if key == "" {
		return rb
	}
	if rb.response.Details == nil {
		rb.response.Details = make(map[string]any)
	}
	rb.response.Details[key] = value
	return rb
}

// WithDetails merges multiple details into the payload.
func (rb *ResponseBuilder) WithDetails(details map[string]any) *ResponseBuilder {
	if len(details) == 0 {
		return rb
	}
	for key, value := range details {
		rb.WithDetail(key, value)
	}
	return rb
}

// Build writes the JSON payload using the provided fiber context.
func (rb *ResponseBuilder) Build(c fiber.Ctx) error {
	rb.ensureDefaults()
	payload := rb.buildPayload()
	if rb.source != nil {
		c.SetContext(context.WithValue(c.Context(), responseErrorContextKey{}, rb.source))
	}
	return c.Status(payload.Code).JSON(payload)
}

// ResponseErrorFromContext returns the original error used to build a handled
// error response, if any. Transport middleware uses it after handlers that
// write an error response and therefore return nil.
func ResponseErrorFromContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	err, _ := ctx.Value(responseErrorContextKey{}).(error)
	return err
}

// BuildResponse finalizes and returns the response value.
func (rb *ResponseBuilder) BuildResponse() Response {
	rb.ensureDefaults()
	return rb.buildPayload()
}

func (rb *ResponseBuilder) buildPayload() Response {
	payload := *rb.response
	payload.Data = normalizeTimeUTC(payload.Data)
	payload.Meta = normalizeTimeUTC(payload.Meta)
	if payload.Details != nil {
		if details, ok := normalizeTimeUTC(payload.Details).(map[string]any); ok {
			payload.Details = details
		}
	}
	return payload
}

// ErrorResponseBuilder specializes the builder for standardized error payloads.
type ErrorResponseBuilder struct {
	*ResponseBuilder
	source error
	ctx    errorContext
}

// NewErrorResponseBuilder creates a builder populated from the provided error.
func NewErrorResponseBuilder(err error) *ErrorResponseBuilder {
	ctx := normalizeError(err)
	builder := NewResponseBuilder().
		WithCode(ctx.status).
		WithMessage(ctx.message).
		WithEntity(ctx.entity).
		WithError(ctx.message).
		WithErrorCode(ctx.errorCode).
		WithErrors(ctx.errors).
		WithDetails(ctx.details)
	builder.source = err

	return &ErrorResponseBuilder{
		ResponseBuilder: builder,
		source:          err,
		ctx:             ctx,
	}
}

// Error exposes the underlying error used to build the response.
func (erb *ErrorResponseBuilder) Error() error {
	return erb.source
}

// Status exposes the derived HTTP status from the underlying error.
func (erb *ErrorResponseBuilder) Status() int {
	return erb.ctx.status
}

type errorContext struct {
	status    int
	message   string
	entity    string
	errorCode string
	errors    any
	details   map[string]any
}

func normalizeError(err error) errorContext {
	ctx := errorContext{
		status:    http.StatusInternalServerError,
		message:   http.StatusText(http.StatusInternalServerError),
		entity:    "system",
		errorCode: "internal_error",
	}

	if err == nil {
		return ctx
	}

	if httpErr, ok := errors.AsType[*HTTPError](err); ok && httpErr != nil {
		ctx.status = httpErr.Status()
		ctx.message = formatHTTPErrorMessage(httpErr)
		ctx.errorCode = httpErr.ErrorCode()
		ctx.entity = firstNonEmpty(httpErr.Entity(), ctx.entity)
		ctx.details = httpErr.Details()
		ctx.errors = httpErr.Errors()
		return ctx
	}

	if fiberErr, ok := errors.AsType[*fiber.Error](err); ok && fiberErr != nil {
		if fiberErr.Code > 0 {
			ctx.status = fiberErr.Code
		}
		if msg := strings.TrimSpace(fiberErr.Message); msg != "" {
			ctx.message = msg
		} else {
			ctx.message = http.StatusText(ctx.status)
		}
		ctx.errorCode = "http_" + strconv.Itoa(ctx.status)
		return ctx
	}

	var statusProvider interface{ Status() int }
	if errors.As(err, &statusProvider) {
		if s := statusProvider.Status(); s > 0 {
			ctx.status = s
		}
	}

	var entityProvider interface{ Entity() string }
	if errors.As(err, &entityProvider) {
		if entity := strings.TrimSpace(entityProvider.Entity()); entity != "" {
			ctx.entity = entity
		}
	}

	var codeProvider interface{ ErrorCode() string }
	if errors.As(err, &codeProvider) {
		if code := strings.TrimSpace(codeProvider.ErrorCode()); code != "" {
			ctx.errorCode = code
		}
	}

	var detailsProvider interface{ Details() map[string]any }
	if errors.As(err, &detailsProvider) {
		ctx.details = copyDetails(detailsProvider.Details())
	}

	var errorsProvider interface{ Errors() []string }
	if errors.As(err, &errorsProvider) {
		ctx.errors = append([]string(nil), errorsProvider.Errors()...)
	}

	if msg := strings.TrimSpace(err.Error()); msg != "" {
		ctx.message = msg
	}

	// Never leak an internal error's raw text (SQL driver strings, provider
	// payloads) to the client at 5xx. Typed errors (*HTTPError, *fiber.Error)
	// return earlier and control their own message; this fallthrough only
	// handles raw errors, whose text is for logs/spans, not clients.
	if ctx.status >= http.StatusInternalServerError {
		ctx.message = http.StatusText(ctx.status)
	}

	return ctx
}

func formatHTTPErrorMessage(httpErr *HTTPError) string {
	if httpErr == nil {
		return http.StatusText(http.StatusInternalServerError)
	}
	// Message(), not Error(): the response must not leak the internal cause.
	return strings.TrimSpace(httpErr.Message())
}

func copyDetails(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	cp := make(map[string]any, len(details))
	for k, v := range details {
		cp[k] = v
	}
	return cp
}

// ExtractValidationErrors converts go-playground validation errors into
func ExtractValidationErrors(err error) any {
	if details := validation.Details(err); len(details) > 0 {
		return details
	}
	return validation.Messages(err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isEmptyErrors(errors any) bool {
	switch value := errors.(type) {
	case nil:
		return true
	case []string:
		return len(value) == 0
	case []validation.ErrorDetail:
		return len(value) == 0
	default:
		return false
	}
}
