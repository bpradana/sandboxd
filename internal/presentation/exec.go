package presentation

import "time"

type ExecRequest struct {
	// Command is run through /bin/sh -c. Exactly one of command or argv is required.
	Command string `json:"command" validate:"required_without=Argv,excluded_with=Argv,max=131072" example:"python3 -c 'print(1+1)'"`
	// Argv is executed directly without a shell; argv[0] is resolved through PATH.
	Argv []string `json:"argv" validate:"required_without=Command,omitempty,min=1,max=4096,dive,max=131072"`
	// User runs the command as root (uid 0 of the sandbox, unprivileged on the
	// host) or the unprivileged sandbox user; defaults to SANDBOX_DEFAULT_USER.
	User string `json:"user" validate:"omitempty,oneof=root sandbox" example:"root"`
	// Cwd is the working directory; defaults to the user's home directory.
	Cwd string `json:"cwd" validate:"omitempty,max=4096" example:"/root"`
	// Env is merged over the sandbox environment for this command only.
	Env map[string]string `json:"env" validate:"omitempty,max=128,dive,keys,notblank,max=256,endkeys,max=4096"`
	// Stdin is written to the command's standard input, which is then closed.
	Stdin string `json:"stdin" validate:"omitempty,max=10485760"`
	// TimeoutSeconds kills the command's whole process group when exceeded.
	TimeoutSeconds int `json:"timeout_seconds" validate:"omitempty,gte=1" example:"60"`
}

type ExecResponse struct {
	// ExitCode is the process exit status, or 128+signal when killed by a signal.
	ExitCode int `json:"exit_code" example:"0"`
	// Signal names the terminating signal, if any.
	Signal          string `json:"signal,omitempty" example:"SIGKILL"`
	Stdout          string `json:"stdout" example:"2\n"`
	Stderr          string `json:"stderr" example:""`
	StdoutTruncated bool   `json:"stdout_truncated" example:"false"`
	StderrTruncated bool   `json:"stderr_truncated" example:"false"`
	TimedOut        bool   `json:"timed_out" example:"false"`
	DurationMs      int64  `json:"duration_ms" example:"12"`
}

// ExecStreamEvent is one Server-Sent Event emitted by the streaming exec
// endpoint: events stdout/stderr carry Data; the final exit event carries Result.
type ExecStreamEvent struct {
	Data   string        `json:"data,omitempty"`
	Result *ExecResponse `json:"result,omitempty"`
	Error  string        `json:"error,omitempty"`
}

type CreateSessionRequest struct {
	// Shell is the interpreter kept alive for the session; bash falls back to sh when absent.
	Shell string `json:"shell" validate:"omitempty,oneof=sh bash" example:"bash"`
	// User runs the session's shell as root or the sandbox user; defaults to
	// SANDBOX_DEFAULT_USER.
	User string `json:"user" validate:"omitempty,oneof=root sandbox" example:"root"`
	// Cwd is the initial working directory.
	Cwd string `json:"cwd" validate:"omitempty,max=4096" example:"/home/sandbox"`
	// Env is exported into the session shell at startup.
	Env map[string]string `json:"env" validate:"omitempty,max=128,dive,keys,notblank,max=256,endkeys,max=4096"`
}

type SessionExecRequest struct {
	// Command is written to the session shell; shell state (cwd, variables) persists afterwards.
	Command string `json:"command" validate:"required,max=131072" example:"cd /tmp && export X=1"`
	// TimeoutSeconds terminates the session when exceeded, because the shell state is then undefined.
	TimeoutSeconds int `json:"timeout_seconds" validate:"omitempty,gte=1" example:"60"`
}

type SessionResponse struct {
	ID             string    `json:"id" example:"ses_01jbq3w4k7m2x9z8a1b2c3d4e5"`
	SandboxID      string    `json:"sandbox_id" example:"sbx_01jbq3w4k7m2x9z8a1b2c3d4e5"`
	Shell          string    `json:"shell" example:"bash"`
	User           string    `json:"user" example:"root"`
	State          string    `json:"state" example:"idle"`
	CreatedAt      time.Time `json:"created_at"`
	LastActivityAt time.Time `json:"last_activity_at"`
}
