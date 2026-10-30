package sandbox

import (
	"context"
	"errors"
	"net/http"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/preview"
)

func (s *service) CreatePreview(ctx context.Context, id string, req presentation.CreatePreviewRequest) (*preview.Session, error) {
	if err := validation.Struct(req); err != nil {
		return nil, appctx.NewHTTPError(http.StatusBadRequest, "provide a preview port between 1 and 65535").WithErrorCode(appctx.CodeValidation)
	}
	if s.previews == nil {
		return nil, appctx.NewHTTPError(http.StatusServiceUnavailable, preview.ErrDisabled.Error()).WithErrorCode(appctx.CodePreviewUnavailable)
	}
	sb, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	if sb.State != consts.SandboxStateRunning {
		return nil, appctx.ErrSandboxNotRunning()
	}
	entry, err := s.previews.Create(id, req.Port, sb.ExpiresAt, func(ctx context.Context) (func(), error) {
		current, err := s.repo.Get(ctx, id)
		if err != nil || current.State != consts.SandboxStateRunning {
			return nil, appctx.ErrSandboxNotRunning()
		}
		return s.beginActivity(ctx, id), nil
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, preview.ErrLimit) {
			status = http.StatusTooManyRequests
		}
		return nil, appctx.NewHTTPError(status, err.Error()).WithErrorCode(appctx.CodePreviewUnavailable)
	}
	s.touch(ctx, id)
	return &entry, nil
}

func (s *service) RevokePreview(_ context.Context, id string, req presentation.RevokePreviewRequest) error {
	if s.previews == nil || !s.previews.Revoke(id, req.ID) {
		return appctx.NewHTTPError(http.StatusNotFound, "preview not found").WithErrorCode(appctx.CodePreviewNotFound)
	}
	return nil
}
