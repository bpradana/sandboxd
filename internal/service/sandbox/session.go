package sandbox

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

func (s *service) CreateSession(ctx context.Context, id string, req presentation.CreateSessionRequest) (*presentation.SessionResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.CreateSession")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	sb, err := s.runningSandbox(ctx, id)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	now := s.now()
	session := &model.Session{
		ID:             helper.NewID("ses"),
		SandboxID:      id,
		Shell:          orDefaultString(req.Shell, "sh"),
		User:           s.user(req.User),
		State:          consts.SessionStateIdle,
		CreatedAt:      now,
		LastActivityAt: now,
	}
	span.SetAttributes(tracing.String(tracing.SessionIDKey, session.ID))

	env := make(map[string]string, len(sb.Env)+len(req.Env))
	for key, value := range sb.Env {
		env[key] = value
	}
	for key, value := range req.Env {
		env[key] = value
	}
	if err := s.engine.OpenSession(ctx, id, session.ID, engine.SessionRequest{
		Shell: session.Shell,
		User:  session.User,
		Cwd:   req.Cwd,
		Env:   env,
	}); err != nil {
		tracing.RecordError(span, err)
		return nil, mapError(err)
	}
	if err := s.repo.CreateSession(ctx, session); err != nil {
		tracing.RecordError(span, err)
		_ = s.engine.CloseSession(context.WithoutCancel(ctx), id, session.ID)
		return nil, mapError(err)
	}
	s.touch(ctx, id)

	resp := toSessionResponse(session)
	return &resp, nil
}

func (s *service) ListSessions(ctx context.Context, id string) ([]presentation.SessionResponse, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, mapError(err)
	}
	sessions, err := s.repo.ListSessions(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]presentation.SessionResponse, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, toSessionResponse(session))
	}
	return result, nil
}

func (s *service) SessionExec(ctx context.Context, id, sessionID string, req presentation.SessionExecRequest) (*presentation.ExecResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.SessionExec")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id), tracing.String(tracing.SessionIDKey, sessionID))

	if _, err := s.runningSandbox(ctx, id); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	timeout, err := s.execTimeout(req.TimeoutSeconds)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if err := s.setSessionState(ctx, id, sessionID, consts.SessionStateBusy); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	output := newCappedOutput(s.exec.MaxOutputBytes)
	done := s.beginActivity(ctx, id)
	result, err := s.engine.SessionExec(ctx, id, sessionID, engine.SessionExecRequest{
		Command: req.Command,
		Timeout: timeout,
	}, output.write)
	done()

	nextState := consts.SessionStateIdle
	if errors.Is(err, engine.ErrSessionClosed) || (result != nil && result.TimedOut) {
		// A timed-out command leaves the shell in an unknown state, so the
		// engine terminates it rather than handing back a poisoned session.
		nextState = consts.SessionStateTerminated
	}
	if stateErr := s.setSessionState(ctx, id, sessionID, nextState); stateErr != nil {
		s.logger.Ctx(ctx).Warn("update session state", zap.String("session_id", sessionID), zap.Error(stateErr))
	}

	if err != nil {
		tracing.RecordError(span, err)
		s.metrics.ObserveExec(consts.ExecKindSession, consts.ExecOutcomeError, 0)
		return nil, mapError(err)
	}
	s.observeResult(span, consts.ExecKindSession, result)
	return output.response(result), nil
}

func (s *service) CloseSession(ctx context.Context, id, sessionID string) error {
	if _, err := s.repo.GetSession(ctx, id, sessionID); err != nil {
		if errors.Is(err, sandboxrepository.ErrNotFound) {
			return mapError(engine.ErrSessionNotFound)
		}
		return mapError(err)
	}
	if err := s.engine.CloseSession(ctx, id, sessionID); err != nil &&
		!errors.Is(err, engine.ErrSessionNotFound) && !errors.Is(err, engine.ErrSandboxNotFound) {
		return mapError(err)
	}
	if err := s.repo.DeleteSession(ctx, id, sessionID); err != nil && !errors.Is(err, sandboxrepository.ErrNotFound) {
		return mapError(err)
	}
	return nil
}

// setSessionState transitions a session. Moving to busy fails when the session
// is already busy or terminated; the repository update makes it atomic.
func (s *service) setSessionState(ctx context.Context, id, sessionID, state string) error {
	_, err := s.repo.UpdateSession(ctx, id, sessionID, func(session *model.Session) error {
		if state == consts.SessionStateBusy {
			switch session.State {
			case consts.SessionStateBusy:
				return engine.ErrSessionBusy
			case consts.SessionStateTerminated:
				return engine.ErrSessionClosed
			}
		}
		session.State = state
		session.LastActivityAt = s.now()
		return nil
	})
	if errors.Is(err, sandboxrepository.ErrNotFound) {
		return mapError(engine.ErrSessionNotFound)
	}
	return mapError(err)
}
