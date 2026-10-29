package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

// createTimeout bounds engine.Create independently of the HTTP request, so a
// client disconnect cannot leave a half-built sandbox behind.
const createTimeout = 2 * time.Minute

func (s *service) ListImages(ctx context.Context) ([]presentation.ImageResponse, error) {
	images, err := s.engine.Images(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]presentation.ImageResponse, 0, len(images))
	for _, image := range images {
		result = append(result, presentation.ImageResponse{Name: image.Name, CreatedAt: image.CreatedAt})
	}
	return result, nil
}

func (s *service) Create(ctx context.Context, req presentation.CreateSandboxRequest) (*presentation.SandboxResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.Create")
	defer span.End()

	if err := s.engine.Ready(); err != nil {
		tracing.RecordError(span, err)
		return nil, mapError(err)
	}

	sb, err := s.newSandbox(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, sb.ID), tracing.String(tracing.SandboxImageKey, sb.Image))

	if err := s.repo.Create(ctx, sb, s.sandbox.MaxSandboxes); err != nil {
		tracing.RecordError(span, err)
		return nil, mapError(err)
	}

	started := s.now()
	createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	defer cancel()
	info, err := s.engine.Create(createCtx, engine.Spec{
		ID:       sb.ID,
		Image:    sb.Image,
		Hostname: "sandbox",
		Network:  sb.Network,
		Limits:   sb.Limits,
		Env:      sb.Env,
	})
	if err != nil {
		tracing.RecordError(span, err)
		s.metrics.SandboxEvent("create_failed")
		s.markStopped(ctx, sb.ID, consts.SandboxStateFailed, "", err.Error())
		s.logger.Ctx(ctx).Error("sandbox create failed", zap.String("sandbox_id", sb.ID), zap.Error(err))
		return nil, mapError(err)
	}

	updated, err := s.repo.Update(ctx, sb.ID, func(record *model.Sandbox) error {
		record.State = consts.SandboxStateRunning
		record.IPAddress = info.IPAddress
		record.LastActivityAt = s.now()
		return nil
	})
	if err != nil {
		tracing.RecordError(span, err)
		return nil, mapError(err)
	}

	s.metrics.SandboxEvent("created")
	s.metrics.SandboxStarted(sb.Network)
	s.metrics.ObserveSandboxCreate(s.now().Sub(started))
	s.logger.Ctx(ctx).Info("sandbox created",
		zap.String("sandbox_id", sb.ID),
		zap.String("image", sb.Image),
		zap.String("network", sb.Network),
		zap.Int("init_pid", info.InitPID),
	)
	return toSandboxResponse(updated), nil
}

// newSandbox applies defaults and enforces the configured maximums.
func (s *service) newSandbox(req presentation.CreateSandboxRequest) (*model.Sandbox, error) {
	limits := model.Limits{
		CPUMillis: orDefault(req.CPUMillis, s.sandbox.DefaultCPUMillis),
		MemoryMB:  orDefault(req.MemoryMB, s.sandbox.DefaultMemoryMB),
		StorageMB: orDefault(req.StorageMB, s.sandbox.DefaultStorageMB),
		PidsMax:   orDefault(req.PidsMax, s.sandbox.DefaultPids),
	}
	checks := []struct {
		field         string
		value, maxVal int
	}{
		{"cpu_millis", limits.CPUMillis, s.sandbox.MaxCPUMillis},
		{"memory_mb", limits.MemoryMB, s.sandbox.MaxMemoryMB},
		{"storage_mb", limits.StorageMB, s.sandbox.MaxStorageMB},
		{"pids_max", limits.PidsMax, s.sandbox.MaxPids},
	}
	for _, check := range checks {
		if check.value > check.maxVal {
			return nil, appctx.ErrLimitExceeded(fmt.Sprintf("%s exceeds the maximum of %d", check.field, check.maxVal))
		}
	}

	network := orDefaultString(req.Network, consts.NetworkNone)
	if network == consts.NetworkEgress && !s.network.Enabled {
		return nil, appctx.ErrLimitExceeded("egress networking is disabled on this server")
	}

	ttl := s.sandbox.DefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl > s.sandbox.MaxTTL {
		return nil, appctx.ErrLimitExceeded(fmt.Sprintf("ttl_seconds exceeds the maximum of %d", int(s.sandbox.MaxTTL.Seconds())))
	}
	idle := s.sandbox.IdleTimeout
	if req.IdleTimeoutSeconds > 0 {
		idle = time.Duration(req.IdleTimeoutSeconds) * time.Second
	}

	now := s.now()
	return &model.Sandbox{
		ID:             helper.NewID("sbx"),
		Image:          orDefaultString(strings.TrimSpace(req.Image), s.sandbox.DefaultImage),
		State:          consts.SandboxStateCreating,
		Network:        network,
		Limits:         limits,
		Env:            req.Env,
		Labels:         req.Labels,
		IdleTimeout:    idle,
		CreatedAt:      now,
		ExpiresAt:      now.Add(ttl),
		LastActivityAt: now,
	}, nil
}

func (s *service) Get(ctx context.Context, id string) (*presentation.SandboxResponse, error) {
	sb, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	resp := toSandboxResponse(sb)
	if sb.State == consts.SandboxStateRunning {
		// Stats are best effort on GET: the sandbox may be torn down concurrently.
		if stats, err := s.engine.Stats(ctx, id); err == nil {
			resp.Stats = toStatsResponse(stats)
		}
	}
	return resp, nil
}

func (s *service) List(ctx context.Context, req presentation.ListSandboxesRequest) ([]presentation.SandboxResponse, error) {
	filter := sandboxrepository.Filter{State: req.State}
	if req.Label != "" {
		key, value, ok := strings.Cut(req.Label, "=")
		if !ok || key == "" {
			return nil, appctx.NewHTTPError(http.StatusBadRequest, "label filter must be key=value").WithErrorCode(appctx.CodeValidation)
		}
		filter.LabelKey, filter.LabelValue = key, value
	}
	sandboxes, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]presentation.SandboxResponse, 0, len(sandboxes))
	for _, sb := range sandboxes {
		result = append(result, *toSandboxResponse(sb))
	}
	return result, nil
}

func (s *service) Stats(ctx context.Context, id string) (*presentation.SandboxStatsResponse, error) {
	if _, err := s.runningSandbox(ctx, id); err != nil {
		return nil, err
	}
	stats, err := s.engine.Stats(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	return toStatsResponse(stats), nil
}

func (s *service) Keepalive(ctx context.Context, id string, req presentation.KeepaliveRequest) (*presentation.SandboxResponse, error) {
	ttl := s.sandbox.DefaultTTL
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	updated, err := s.repo.Update(ctx, id, func(sb *model.Sandbox) error {
		if sb.State != consts.SandboxStateRunning {
			return appctx.ErrSandboxNotRunning()
		}
		now := s.now()
		expiresAt := now.Add(ttl)
		if limit := sb.CreatedAt.Add(s.sandbox.MaxTTL); expiresAt.After(limit) {
			expiresAt = limit
		}
		sb.ExpiresAt = expiresAt
		sb.LastActivityAt = now
		return nil
	})
	if err != nil {
		return nil, mapError(err)
	}
	return toSandboxResponse(updated), nil
}

func (s *service) Destroy(ctx context.Context, id string) error {
	return s.destroy(ctx, id, consts.StopReasonDeleted)
}

// destroy moves a sandbox to stopping, releases its kernel resources, and
// leaves a stopped tombstone. Destroying an already stopped sandbox is a no-op.
func (s *service) destroy(ctx context.Context, id, reason string) error {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.Destroy")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	errAlreadyStopping := errors.New("already stopping")
	sb, err := s.repo.Update(ctx, id, func(record *model.Sandbox) error {
		if !record.Active() || record.State == consts.SandboxStateStopping {
			return errAlreadyStopping
		}
		record.State = consts.SandboxStateStopping
		return nil
	})
	if errors.Is(err, errAlreadyStopping) {
		return nil
	}
	if err != nil {
		tracing.RecordError(span, err)
		return mapError(err)
	}

	destroyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.sandbox.DestroyTimeout)
	defer cancel()
	if err := s.engine.Destroy(destroyCtx, id); err != nil && !errors.Is(err, engine.ErrSandboxNotFound) {
		tracing.RecordError(span, err)
		s.logger.Ctx(ctx).Error("sandbox destroy failed", zap.String("sandbox_id", id), zap.Error(err))
		s.markStopped(ctx, id, consts.SandboxStateFailed, reason, err.Error())
		s.metrics.SandboxStopped(sb.Network)
		s.metrics.SandboxEvent("destroy_failed")
		return mapError(err)
	}

	s.markStopped(ctx, id, consts.SandboxStateStopped, reason, "")
	s.metrics.SandboxStopped(sb.Network)
	s.metrics.SandboxEvent(reason)
	s.logger.Ctx(ctx).Info("sandbox destroyed", zap.String("sandbox_id", id), zap.String("reason", reason))
	return nil
}

func (s *service) markStopped(ctx context.Context, id, state, reason, failure string) {
	_, err := s.repo.Update(ctx, id, func(sb *model.Sandbox) error {
		now := s.now()
		sb.State = state
		sb.StoppedAt = &now
		if reason != "" {
			sb.StopReason = reason
		}
		if failure != "" {
			sb.FailureReason = failure
		}
		return nil
	})
	if err != nil && !errors.Is(err, sandboxrepository.ErrNotFound) {
		s.logger.Ctx(ctx).Error("mark sandbox stopped", zap.String("sandbox_id", id), zap.Error(err))
	}
}

// runningSandbox loads a sandbox and requires it to be running.
func (s *service) runningSandbox(ctx context.Context, id string) (*model.Sandbox, error) {
	sb, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	if sb.State != consts.SandboxStateRunning {
		return nil, appctx.ErrSandboxNotRunning()
	}
	return sb, nil
}

// touch records activity so the idle reaper keeps the sandbox alive.
func (s *service) touch(ctx context.Context, id string) {
	_, _ = s.repo.Update(ctx, id, func(sb *model.Sandbox) error {
		sb.LastActivityAt = s.now()
		return nil
	})
}

// beginActivity marks work in progress until the returned func is called.
// Activity is recorded at both ends, so a long command neither looks idle
// while it runs nor right after it finishes.
func (s *service) beginActivity(ctx context.Context, id string) func() {
	s.touch(ctx, id)
	s.inFlightMu.Lock()
	s.inFlight[id]++
	s.inFlightMu.Unlock()
	return func() {
		s.inFlightMu.Lock()
		if s.inFlight[id]--; s.inFlight[id] <= 0 {
			delete(s.inFlight, id)
		}
		s.inFlightMu.Unlock()
		s.touch(ctx, id)
	}
}

func (s *service) busy(id string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	return s.inFlight[id] > 0
}

func orDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func orDefaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
