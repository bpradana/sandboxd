package sandbox

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *service) Exec(ctx context.Context, id string, req presentation.ExecRequest) (*presentation.ExecResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.Exec")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	execReq, err := s.prepareExec(ctx, id, req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	output := newCappedOutput(s.exec.MaxOutputBytes)
	done := s.beginActivity(ctx, id)
	result, err := s.engine.Exec(ctx, id, execReq, output.write)
	done()
	if err != nil {
		tracing.RecordError(span, err)
		s.metrics.ObserveExec(consts.ExecKindExec, consts.ExecOutcomeError, 0)
		return nil, mapError(err)
	}

	s.observeResult(span, consts.ExecKindExec, result)
	return output.response(result), nil
}

func (s *service) ExecStream(ctx context.Context, id string, req presentation.ExecRequest, emit StreamEmitter) error {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.ExecStream")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	execReq, err := s.prepareExec(ctx, id, req)
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}

	done := s.beginActivity(ctx, id)
	result, err := s.engine.Exec(ctx, id, execReq, func(stream engine.Stream, data []byte) error {
		return emit(stream.String(), presentation.ExecStreamEvent{Data: string(data)})
	})
	done()
	if err != nil {
		tracing.RecordError(span, err)
		s.metrics.ObserveExec(consts.ExecKindStream, consts.ExecOutcomeError, 0)
		return emit("error", presentation.ExecStreamEvent{Error: clientMessage(err)})
	}

	s.observeResult(span, consts.ExecKindStream, result)
	return emit("exit", presentation.ExecStreamEvent{Result: &presentation.ExecResponse{
		ExitCode:   result.ExitCode,
		Signal:     result.Signal,
		TimedOut:   result.TimedOut,
		DurationMs: result.Duration.Milliseconds(),
	}})
}

// prepareExec validates the request against a running sandbox and resolves
// argv, environment, and timeout.
func (s *service) prepareExec(ctx context.Context, id string, req presentation.ExecRequest) (engine.ExecRequest, error) {
	sb, err := s.runningSandbox(ctx, id)
	if err != nil {
		return engine.ExecRequest{}, err
	}

	timeout, err := s.execTimeout(req.TimeoutSeconds)
	if err != nil {
		return engine.ExecRequest{}, err
	}

	argv := req.Argv
	if req.Command != "" {
		argv = []string{"/bin/sh", "-c", req.Command}
	}
	if len(argv) == 0 || argv[0] == "" {
		return engine.ExecRequest{}, appctx.NewHTTPError(http.StatusBadRequest, "command or argv is required").WithErrorCode(appctx.CodeValidation)
	}

	env := make(map[string]string, len(sb.Env)+len(req.Env))
	maps.Copy(env, sb.Env)
	maps.Copy(env, req.Env)

	return engine.ExecRequest{
		Argv:    argv,
		User:    s.user(req.User),
		Env:     env,
		Cwd:     req.Cwd,
		Stdin:   []byte(req.Stdin),
		Timeout: timeout,
	}, nil
}

func (s *service) execTimeout(seconds int) (time.Duration, error) {
	if seconds <= 0 {
		return s.exec.DefaultTimeout, nil
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > s.exec.MaxTimeout {
		return 0, appctx.ErrLimitExceeded(fmt.Sprintf("timeout_seconds exceeds the maximum of %d", int(s.exec.MaxTimeout.Seconds())))
	}
	return timeout, nil
}

func (s *service) observeResult(span trace.Span, kind string, result *engine.ExecResult) {
	outcome := consts.ExecOutcomeOK
	switch {
	case result.TimedOut:
		outcome = consts.ExecOutcomeTimeout
	case result.ExitCode != 0:
		outcome = consts.ExecOutcomeNonZero
	}
	s.metrics.ObserveExec(kind, outcome, result.Duration)
	span.SetAttributes(
		tracing.Int(tracing.ExecExitCodeKey, result.ExitCode),
		tracing.Bool(tracing.ExecTimedOutKey, result.TimedOut),
	)
}

// clientMessage is the client-safe message of a service error.
func clientMessage(err error) string {
	if httpErr, ok := errors.AsType[*appctx.HTTPError](mapError(err)); ok {
		return httpErr.Message()
	}
	return "internal error"
}

// cappedOutput collects stdout/stderr up to a per-stream byte cap. Output past
// the cap is discarded (the command keeps running) and flagged as truncated.
type cappedOutput struct {
	limit           int
	stdout, stderr  []byte
	stdoutTruncated bool
	stderrTruncated bool
}

func newCappedOutput(limit int) *cappedOutput {
	return &cappedOutput{limit: limit}
}

func (o *cappedOutput) write(stream engine.Stream, data []byte) error {
	buf, truncated := &o.stdout, &o.stdoutTruncated
	if stream == engine.Stderr {
		buf, truncated = &o.stderr, &o.stderrTruncated
	}
	room := o.limit - len(*buf)
	if room <= 0 {
		*truncated = *truncated || len(data) > 0
		return nil
	}
	if len(data) > room {
		data = data[:room]
		*truncated = true
	}
	*buf = append(*buf, data...)
	return nil
}

func (o *cappedOutput) response(result *engine.ExecResult) *presentation.ExecResponse {
	return &presentation.ExecResponse{
		ExitCode:        result.ExitCode,
		Signal:          result.Signal,
		Stdout:          string(o.stdout),
		Stderr:          string(o.stderr),
		StdoutTruncated: o.stdoutTruncated,
		StderrTruncated: o.stderrTruncated,
		TimedOut:        result.TimedOut,
		DurationMs:      result.Duration.Milliseconds(),
	}
}

// user resolves a requested user against SANDBOX_DEFAULT_USER.
func (s *service) user(requested string) string {
	if requested != "" {
		return requested
	}
	if s.sandbox.DefaultUser != "" {
		return s.sandbox.DefaultUser
	}
	return consts.UserSandbox
}
