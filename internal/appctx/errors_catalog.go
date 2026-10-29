package appctx

import "net/http"

// Catalog constructors for HTTPErrors built at several call sites. Each returns a
// fresh, unshared *HTTPError (callers may chain .WithCause(err) to attach the
// underlying error for logs/spans without leaking it to the client). One-off
// errors stay as plain NewHTTPError(...).WithErrorCode(...) at their site.

func ErrUnauthorized() *HTTPError {
	return NewHTTPError(http.StatusUnauthorized, "missing or invalid api key").WithErrorCode(CodeUnauthorized)
}

func ErrSandboxNotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, "sandbox not found").WithErrorCode(CodeSandboxNotFound)
}

func ErrSessionNotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, "session not found").WithErrorCode(CodeSessionNotFound)
}

func ErrImageNotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, "image not found").WithErrorCode(CodeImageNotFound)
}

func ErrPathNotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, "path not found").WithErrorCode(CodePathNotFound)
}

func ErrSandboxNotRunning() *HTTPError {
	return NewHTTPError(http.StatusConflict, "sandbox is not running").WithErrorCode(CodeSandboxNotRunning)
}

func ErrSessionBusy() *HTTPError {
	return NewHTTPError(http.StatusConflict, "session is already running a command").WithErrorCode(CodeSessionBusy)
}

func ErrSessionClosed() *HTTPError {
	return NewHTTPError(http.StatusConflict, "session is closed").WithErrorCode(CodeSessionClosed)
}

func ErrInvalidPath() *HTTPError {
	return NewHTTPError(http.StatusBadRequest, "invalid path").WithErrorCode(CodeInvalidPath)
}

func ErrPathConflict(message string) *HTTPError {
	return NewHTTPError(http.StatusConflict, message).WithErrorCode(CodePathConflict)
}

func ErrLimitExceeded(message string) *HTTPError {
	return NewHTTPError(http.StatusBadRequest, message).WithErrorCode(CodeLimitExceed)
}

func ErrFileTooLarge() *HTTPError {
	return NewHTTPError(http.StatusRequestEntityTooLarge, "file exceeds maximum allowed size").WithErrorCode(CodeFileTooLarge)
}

func ErrStorageFull() *HTTPError {
	return NewHTTPError(http.StatusInsufficientStorage, "sandbox storage is full").WithErrorCode(CodeStorageFull)
}

func ErrSandboxLimitReached() *HTTPError {
	return NewHTTPError(http.StatusTooManyRequests, "maximum number of sandboxes reached").WithErrorCode(CodeSandboxLimitReached)
}

func ErrExecLimitReached() *HTTPError {
	return NewHTTPError(http.StatusTooManyRequests, "maximum number of concurrent commands reached").WithErrorCode(CodeExecLimitReached)
}

func ErrTerminalNotFound() *HTTPError {
	return NewHTTPError(http.StatusNotFound, "terminal not found").WithErrorCode(CodeTerminalNotFound)
}

func ErrTerminalTaken() *HTTPError {
	return NewHTTPError(http.StatusConflict, "terminal attached in another window").WithErrorCode(CodeTerminalTaken)
}

func ErrTerminalLimitReached() *HTTPError {
	return NewHTTPError(http.StatusTooManyRequests, "maximum number of terminals reached").WithErrorCode(CodeTerminalLimitReached)
}

func ErrInvalidTerminalTicket() *HTTPError {
	return NewHTTPError(http.StatusUnauthorized, "missing, invalid, or expired terminal ticket").WithErrorCode(CodeUnauthorized)
}

func ErrOriginNotAllowed() *HTTPError {
	return NewHTTPError(http.StatusForbidden, "origin not allowed").WithErrorCode(CodeForbidden)
}

func ErrEngineUnavailable() *HTTPError {
	return NewHTTPError(http.StatusServiceUnavailable, "sandbox engine unavailable").WithErrorCode(CodeEngineUnavailable)
}

func ErrUnsupportedPlatform() *HTTPError {
	return NewHTTPError(http.StatusServiceUnavailable, "sandbox engine requires linux").WithErrorCode(CodeUnsupportedPlatform)
}

func ErrInternal() *HTTPError {
	return NewHTTPError(http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError)).WithErrorCode(CodeInternal)
}
