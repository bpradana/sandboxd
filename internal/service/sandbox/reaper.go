package sandbox

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/helper/safego"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

// reapConcurrency bounds how many expired sandboxes are torn down at once.
const reapConcurrency = 4

func (s *service) startBackground() {
	s.background.Add(2)
	go func() {
		defer s.background.Done()
		s.runReaper()
	}()
	go func() {
		defer s.background.Done()
		s.watchExits()
	}()
}

func (s *service) stopBackground() {
	close(s.stop)
	s.background.Wait()
}

func (s *service) runReaper() {
	ticker := time.NewTicker(s.sandbox.ReaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			_ = safego.Run(context.Background(), s.logger, func() error {
				s.reapOnce(context.Background())
				return nil
			})
		}
	}
}

// reapOnce destroys sandboxes past their TTL or idle timeout and purges old
// tombstones.
func (s *service) reapOnce(ctx context.Context) {
	ctx, span := tracing.SpanStartDetached(ctx, "service.sandbox.Reap")
	defer span.End()

	running, err := s.repo.List(ctx, sandboxrepository.Filter{State: consts.SandboxStateRunning})
	if err != nil {
		tracing.RecordError(span, err)
		return
	}

	now := s.now()
	s.tickets.sweep(now)
	sem := make(chan struct{}, reapConcurrency)
	done := make(chan struct{})
	pending := 0
	for _, sb := range running {
		var reason string
		switch {
		case now.After(sb.ExpiresAt):
			reason = consts.StopReasonExpired
		case sb.IdleTimeout > 0 && now.Sub(sb.LastActivityAt) > sb.IdleTimeout && !s.busy(sb.ID):
			reason = consts.StopReasonIdle
		default:
			continue
		}
		pending++
		sem <- struct{}{}
		go func(id, reason string) {
			defer func() { <-sem; done <- struct{}{} }()
			_ = safego.Run(ctx, s.logger, func() error {
				if err := s.destroy(ctx, id, reason); err != nil {
					s.logger.Ctx(ctx).Warn("reap sandbox", zap.String("sandbox_id", id), zap.Error(err))
				}
				return nil
			})
		}(sb.ID, reason)
	}
	for range pending {
		<-done
	}

	// A zero TTL purges tombstones on the next pass.
	s.repo.PurgeStopped(ctx, now.Add(-s.sandbox.TombstoneTTL))
}

// watchExits marks sandboxes whose init died unexpectedly as failed and
// releases whatever kernel resources remain.
func (s *service) watchExits() {
	exits := s.engine.Exits()
	for {
		select {
		case <-s.stop:
			return
		case exit, ok := <-exits:
			if !ok {
				return
			}
			_ = safego.Run(context.Background(), s.logger, func() error {
				s.handleExit(context.Background(), exit.SandboxID, exit.Err)
				return nil
			})
		}
	}
}

func (s *service) handleExit(ctx context.Context, id string, cause error) {
	ctx, span := tracing.SpanStartDetached(ctx, "service.sandbox.HandleExit")
	defer span.End()

	message := "sandbox init process exited"
	if cause != nil {
		message += ": " + cause.Error()
	}
	s.logger.Ctx(ctx).Warn("sandbox init exited unexpectedly", zap.String("sandbox_id", id), zap.Error(cause))
	if err := s.destroy(ctx, id, consts.StopReasonExited); err != nil {
		s.logger.Ctx(ctx).Warn("cleanup exited sandbox", zap.String("sandbox_id", id), zap.Error(err))
	}
	s.markStopped(ctx, id, consts.SandboxStateFailed, consts.StopReasonExited, message)
	s.metrics.SandboxEvent("failed")
}
