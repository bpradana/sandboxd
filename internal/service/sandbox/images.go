package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

// ListImages reports what this host can start a sandbox from: the images it
// already holds, and the ones in cloud storage it would fetch on first use.
// Both are usable; only the first is instant.
func (s *service) ListImages(ctx context.Context) ([]presentation.ImageResponse, error) {
	images, err := s.engine.Images(ctx)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]presentation.ImageResponse, 0, len(images))
	local := make(map[string]bool, len(images))
	for _, image := range images {
		local[image.Name] = true
		result = append(result, presentation.ImageResponse{Name: image.Name, CreatedAt: image.CreatedAt, Local: true})
	}

	// Cloud images are advisory: losing storage must not stop a host listing
	// what it can already build.
	for _, ref := range s.imageRefs(ctx) {
		if local[ref.Name] {
			continue
		}
		result = append(result, presentation.ImageResponse{
			Name: ref.Name, CreatedAt: ref.CreatedAt, Local: false, Architecture: ref.Architecture,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// imageRefs reads the cluster's promoted images, or nothing at all when
// cloud storage is absent or unreachable.
func (s *service) imageRefs(ctx context.Context) []snapshot.ImageRef {
	s.snapshotStoreMu.RLock()
	store := s.snapshots
	s.snapshotStoreMu.RUnlock()
	if store == nil {
		return nil
	}
	refs, err := store.ListImageRefs(ctx)
	if err != nil {
		s.logger.Ctx(ctx).Warn("list cloud images", zap.Error(err))
		return nil
	}
	return refs
}

// materializeImage builds a base image this host has never held, from the
// snapshot a promotion pointed at. It is what makes one host interchangeable
// with another: a sandbox can be placed anywhere, and the first one to ask
// for an image pays for it once on that host.
func (s *service) materializeImage(ctx context.Context, name string) error {
	release, err := s.reserveSnapshot()
	if err != nil {
		return err
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, s.snapshotCfg.Timeout)
	defer cancel()

	ref, err := s.snapshots.GetImageRef(ctx, name)
	if err != nil {
		return snapshotStorageError(err)
	}
	if ref.Architecture != runtime.GOARCH {
		return appctx.ErrLimitExceeded("image " + name + " was promoted on another architecture")
	}
	manifest, err := s.snapshots.GetManifest(ctx, ref.SnapshotID)
	if err != nil {
		return snapshotStorageError(err)
	}
	if manifest.EffectiveStatus(s.now()) != snapshot.Ready {
		return snapshotNotReady()
	}

	file, err := s.snapshotTemp()
	if err != nil {
		return mapError(err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()

	s.jobProgress.enter(manifest.ID, operationPromote, phaseDownloading, 0, manifest.SizeBytes)
	defer s.jobProgress.done(manifest.ID)
	downloadCtx := snapshot.WithReporter(ctx, s.jobProgress.reporter(manifest.ID, phaseDownloading))
	if err := s.snapshots.Download(downloadCtx, manifest, file); err != nil {
		return snapshotStorageError(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return mapError(err)
	}

	s.jobProgress.enter(manifest.ID, operationPromote, phaseImporting, manifest.SizeBytes, manifest.SizeBytes)
	if _, err := s.engine.ImportImage(ctx, name, file, s.snapshotCfg.MaxBytes); err != nil {
		// Another sandbox asked for the same image at the same time and won.
		// Both callers wanted the image to exist, and it does.
		if errors.Is(err, engine.ErrImageExists) {
			return nil
		}
		return mapError(err)
	}
	s.logger.Ctx(ctx).Info("materialized base image from cloud storage",
		zap.String("image", name), zap.String("snapshot_id", manifest.ID))
	return nil
}

// RemoveImage deletes a base image from this host and, if it was promoted,
// from cloud storage too, so it stops being materialized elsewhere.
func (s *service) RemoveImage(ctx context.Context, name string) error {
	if !engine.ValidImageName(name) {
		return appctx.ErrLimitExceeded("image name must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	localErr := s.engine.RemoveImage(ctx, name)
	if localErr != nil && !errors.Is(localErr, engine.ErrImageNotFound) {
		return mapError(localErr)
	}

	s.snapshotStoreMu.RLock()
	store := s.snapshots
	s.snapshotStoreMu.RUnlock()
	if store == nil {
		return mapError(localErr)
	}
	if _, err := store.GetImageRef(ctx, name); err != nil {
		if errors.Is(err, snapshot.ErrNotFound) {
			return mapError(localErr)
		}
		return snapshotStorageError(err)
	}
	return snapshotStorageError(store.DeleteImageRef(ctx, name))
}

// imageRefsFor reports which promoted images are built on this snapshot, so
// deleting it cannot quietly strand a name other hosts still resolve.
func (s *service) imageRefsFor(ctx context.Context, snapshotID string) []string {
	var names []string
	for _, ref := range s.imageRefs(ctx) {
		if ref.SnapshotID == snapshotID {
			names = append(names, ref.Name)
		}
	}
	sort.Strings(names)
	return names
}

func imageInUseError(names []string) error {
	message := "snapshot backs base image " + names[0]
	if len(names) > 1 {
		message = "snapshot backs base images " + joinNames(names)
	}
	return appctx.NewHTTPError(http.StatusConflict, message+"; delete the image first").
		WithErrorCode(appctx.CodePathConflict)
}

func joinNames(names []string) string {
	out := ""
	for i, name := range names {
		switch {
		case i == 0:
			out = name
		case i == len(names)-1:
			out += " and " + name
		default:
			out += ", " + name
		}
	}
	return out
}
