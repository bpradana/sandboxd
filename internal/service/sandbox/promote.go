package sandbox

import (
	"context"
	"io"
	"net/http"
	"os"
	"runtime"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

// PromoteSnapshot turns a stored snapshot into a base image, so the work that
// went into one sandbox -- its installed packages, its checked-out source,
// whatever took minutes to prepare -- becomes the starting point of every
// sandbox after it, at no cost per start.
//
// It is the restore path without the sandbox: the same archive, the same
// checks that it belongs on this host, and the same confined extraction,
// committed under an image name instead of a private lower layer.
func (s *service) PromoteSnapshot(ctx context.Context, id string, req presentation.PromoteSnapshotRequest) (*presentation.ImageResponse, error) {
	if err := validation.Struct(req); err != nil {
		return nil, appctx.NewHTTPError(http.StatusBadRequest, "provide an image name").WithErrorCode(appctx.CodeValidation)
	}
	// Refuse an unusable name before anything is downloaded on its behalf.
	if !engine.ValidImageName(req.Image) {
		return nil, appctx.ErrLimitExceeded("image name must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	release, err := s.reserveSnapshot()
	if err != nil {
		return nil, err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, s.snapshotCfg.Timeout)
	defer cancel()

	manifest, err := s.snapshots.GetManifest(ctx, id)
	if err != nil {
		return nil, snapshotStorageError(err)
	}
	if manifest.EffectiveStatus(s.now()) != snapshot.Ready {
		return nil, snapshotNotReady()
	}
	if manifest.Version != snapshot.Version || manifest.Architecture != runtime.GOARCH {
		return nil, appctx.ErrLimitExceeded("snapshot format or architecture is incompatible with this host")
	}

	file, err := s.snapshotTemp()
	if err != nil {
		return nil, mapError(err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()

	s.jobProgress.enter(manifest.ID, operationPromote, phaseDownloading, 0, manifest.SizeBytes)
	defer s.jobProgress.done(manifest.ID)
	downloadCtx := snapshot.WithReporter(ctx, s.jobProgress.reporter(manifest.ID, phaseDownloading))
	if err := s.snapshots.Download(downloadCtx, manifest, file); err != nil {
		return nil, snapshotStorageError(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, mapError(err)
	}

	s.jobProgress.enter(manifest.ID, operationPromote, phaseImporting, manifest.SizeBytes, manifest.SizeBytes)
	created, err := s.engine.ImportImage(ctx, req.Image, file, s.snapshotCfg.MaxBytes)
	if err != nil {
		return nil, mapError(err)
	}
	s.logger.Info("promoted snapshot to base image",
		zap.String("snapshot_id", manifest.ID), zap.String("image", created.Name))
	return &presentation.ImageResponse{Name: created.Name, CreatedAt: created.CreatedAt}, nil
}
