package tracing

const (
	EventLevelKey = "event.level"

	SandboxIDKey    = "sandbox.id"
	SandboxImageKey = "sandbox.image"
	SessionIDKey    = "sandbox.session.id"
	ExecExitCodeKey = "sandbox.exec.exit_code"
	ExecTimedOutKey = "sandbox.exec.timed_out"
	FilePathKey     = "sandbox.file.path"
)

// error.code values assigned by RecordError's fallback classifier for errors
// that don't self-describe via an ErrorCode() method. Keep these
// low-cardinality.
const (
	ErrorCodeCanceled         = "canceled"
	ErrorCodeDeadlineExceeded = "deadline_exceeded"
	ErrorCodeNetwork          = "network"
	ErrorCodeUnclassified     = "unclassified"
)
