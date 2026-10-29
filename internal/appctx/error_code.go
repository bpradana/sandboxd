package appctx

// Stable, low-cardinality error codes for HTTP errors. Set via HTTPError.WithErrorCode so
// they surface as error.type / error.code on traces (see HTTPError.ErrorType) and give
// clients a machine-readable discriminator that does not depend on the message text.
//
// Codes are snake_case and describe the failure, not the HTTP status. Add new ones here
// rather than passing string literals at call sites. Errors without a code fall back to
// "http_<status>".
const (
	// auth (401)
	CodeUnauthorized = "unauthorized" // missing/invalid bearer API key or terminal ticket

	// forbidden (403)
	CodeForbidden = "forbidden" // e.g. a terminal upgrade from a foreign Origin

	// not found (404)
	CodeSandboxNotFound  = "sandbox_not_found"
	CodeSessionNotFound  = "session_not_found"
	CodeTerminalNotFound = "terminal_not_found" // ended, timed out while detached, or never existed
	CodeImageNotFound    = "image_not_found"
	CodePathNotFound     = "path_not_found"

	// conflict (409)
	CodeSandboxNotRunning = "sandbox_not_running" // sandbox is creating, stopping, or gone
	CodeSessionBusy       = "session_busy"        // session is already running a command
	CodeSessionClosed     = "session_closed"      // session shell exited or was terminated
	CodeTerminalTaken     = "terminal_taken"      // a newer client attached to the terminal
	CodePathConflict      = "path_conflict"       // e.g. reading a directory as a file

	// bad request / validation (400)
	CodeValidation  = "validation_error" // generic request validation failure
	CodeInvalidPath = "invalid_path"     // path is empty, relative, or not resolvable inside the sandbox
	CodeLimitExceed = "limit_exceeded"   // requested resource exceeds the configured maximum

	// payload (413)
	CodeFileTooLarge = "file_too_large"

	// storage (507)
	CodeStorageFull = "storage_full" // the sandbox writable layer hit storage_mb

	// rate limiting / capacity (429)
	CodeSandboxLimitReached  = "sandbox_limit_reached"  // global sandbox cap reached
	CodeExecLimitReached     = "exec_limit_reached"     // per-sandbox concurrent exec cap reached
	CodeTerminalLimitReached = "terminal_limit_reached" // per-sandbox terminal cap reached

	// engine / internal
	CodeEngineUnavailable   = "engine_unavailable"   // preflight failed or engine shutting down (503)
	CodeUnsupportedPlatform = "unsupported_platform" // engine requires Linux (503)
	CodeInternal            = "internal_error"       // unexpected internal failure (500)
)
