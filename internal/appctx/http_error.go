package appctx

import (
	"fmt"
	"net/http"
	"strings"
)

// HTTPError represents an HTTP-aware error without coupling services to a web framework.
type HTTPError struct {
	status    int
	message   string
	entity    string
	errorCode string
	errors    any
	details   map[string]any
	cause     error
}

func NewHTTPError(status int, message string) *HTTPError {
	return &HTTPError{
		status:  status,
		message: strings.TrimSpace(message),
	}
}

// Error includes the wrapped cause so boundary logs carry the root cause;
// client responses use Message() instead.
func (e *HTTPError) Error() string {
	msg := e.Message()
	if e != nil && e.cause != nil {
		return msg + ": " + e.cause.Error()
	}
	return msg
}

// Message is the client-safe response message, without the internal cause.
func (e *HTTPError) Message() string {
	if e == nil {
		return http.StatusText(http.StatusInternalServerError)
	}
	if msg := strings.TrimSpace(e.message); msg != "" {
		return msg
	}
	if errors, ok := e.errors.([]string); ok && len(errors) > 0 {
		return strings.TrimSpace(errors[0])
	}
	if e.status > 0 {
		return http.StatusText(e.status)
	}
	return http.StatusText(http.StatusInternalServerError)
}

// WithCause attaches the underlying error for logs and errors.Is/As without
// leaking it into the client response.
func (e *HTTPError) WithCause(cause error) *HTTPError {
	if e == nil {
		return nil
	}
	e.cause = cause
	return e
}

func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *HTTPError) Status() int {
	if e == nil || e.status <= 0 {
		return http.StatusInternalServerError
	}
	return e.status
}

func (e *HTTPError) Entity() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.entity)
}

func (e *HTTPError) ErrorCode() string {
	if e == nil {
		return ""
	}
	if code := strings.TrimSpace(e.errorCode); code != "" {
		return code
	}
	return fmt.Sprintf("http_%d", e.Status())
}

// ErrorType reports a stable, low-cardinality type for tracing/error grouping.
// Honored by OTel's semconv.ErrorType (OTLP path) and surfaced as error.code on the
// Datadog bridge, so HTTP errors group by code instead of the generic *errors.errorString.
func (e *HTTPError) ErrorType() string {
	return e.ErrorCode()
}

func (e *HTTPError) Errors() any {
	if e == nil || isEmptyErrors(e.errors) {
		return nil
	}
	return e.errors
}

func (e *HTTPError) Details() map[string]any {
	if e == nil || len(e.details) == 0 {
		return nil
	}
	return copyDetails(e.details)
}

func (e *HTTPError) WithEntity(entity string) *HTTPError {
	if e == nil {
		return nil
	}
	e.entity = strings.TrimSpace(entity)
	return e
}

func (e *HTTPError) WithErrorCode(code string) *HTTPError {
	if e == nil {
		return nil
	}
	e.errorCode = strings.TrimSpace(code)
	return e
}

func (e *HTTPError) WithErrors(errors any) *HTTPError {
	if e == nil || isEmptyErrors(errors) {
		return e
	}
	e.errors = errors
	if msg := firstErrorMessage(errors); strings.TrimSpace(e.message) == "" && msg != "" {
		e.message = msg
	}
	return e
}

func firstErrorMessage(errors any) string {
	if list, ok := errors.([]string); ok && len(list) > 0 {
		return strings.TrimSpace(list[0])
	}
	return ""
}

func (e *HTTPError) WithDetails(details map[string]any) *HTTPError {
	if e == nil || len(details) == 0 {
		return e
	}
	e.details = copyDetails(details)
	return e
}
