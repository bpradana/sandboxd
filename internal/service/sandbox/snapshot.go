package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper"
	"github.com/bpradana/sandboxd/internal/helper/safego"
	"github.com/bpradana/sandboxd/internal/helper/validation"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func (s *service) snapshotEnabled() error {
	if s.snapshots == nil {
		return appctx.NewHTTPError(http.StatusServiceUnavailable, "filesystem snapshots are disabled").WithErrorCode(appctx.CodeSnapshotDisabled)
	}
	return nil
}

// reserveSnapshot bounds local disk, network and compression work, including
// restores. Admission and shutdown share a lock so Wait cannot race Add.
func (s *service) reserveSnapshot() (func(), error) {
	s.snapshotStoreMu.RLock()
	if err := s.snapshotEnabled(); err != nil {
		s.snapshotStoreMu.RUnlock()
		return nil, err
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if s.snapshotClosing {
		s.snapshotStoreMu.RUnlock()
		return nil, appctx.ErrEngineUnavailable()
	}
	select {
	case s.snapshotSlots <- struct{}{}:
		s.snapshotJobs.Add(1)
		return func() { <-s.snapshotSlots; s.snapshotStoreMu.RUnlock(); s.snapshotJobs.Done() }, nil
	default:
		s.snapshotStoreMu.RUnlock()
		return nil, appctx.NewHTTPError(http.StatusTooManyRequests, "maximum concurrent snapshot operations reached").WithErrorCode(appctx.CodeSnapshotLimitReached)
	}
}

func (s *service) stopSnapshots(ctx context.Context) error {
	s.snapshotMu.Lock()
	s.snapshotClosing = true
	s.snapshotMu.Unlock()
	done := make(chan struct{})
	go func() { s.snapshotJobs.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *service) snapshotTemp() (*os.File, error) {
	if err := os.MkdirAll(s.snapshotCfg.TempDir, 0o700); err != nil {
		return nil, err
	}
	return os.CreateTemp(s.snapshotCfg.TempDir, "snapshot-*.tar.gz")
}

// The state directory has one daemon owner, like the engine's runtime dirs.
// Crash-interrupted scratch archives are never valid cloud snapshots.
func (s *service) cleanupSnapshotTemps() error {
	if s.snapshots == nil {
		return nil
	}
	entries, err := os.ReadDir(s.snapshotCfg.TempDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "snapshot-") && strings.HasSuffix(entry.Name(), ".tar.gz") {
			if err := os.Remove(filepath.Join(s.snapshotCfg.TempDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *service) CreateSnapshot(ctx context.Context, id string, req presentation.CreateSnapshotRequest) (*presentation.SnapshotResponse, error) {
	release, err := s.reserveSnapshot()
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			release()
		}
	}()
	if err := s.engine.Ready(); err != nil {
		return nil, mapError(err)
	}
	sb, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, mapError(err)
	}
	if !canSnapshot(sb) || req.KeepRunning && sb.State != consts.SandboxStateRunning {
		return nil, appctx.ErrSandboxNotRunning()
	}
	file, err := s.snapshotTemp()
	if err != nil {
		return nil, mapError(err)
	}
	defer func() {
		if owned {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	manifest := snapshot.Manifest{
		Mode: "save_stop", Trigger: req.Trigger,
		Version: snapshot.Version, ID: helper.NewID("snap"), SandboxID: id,
		Status: snapshot.Capturing, CreatedAt: s.now(), DeadlineAt: s.now().Add(s.snapshotCfg.Timeout),
		Architecture: runtime.GOARCH, Labels: maps.Clone(req.Labels),
		Config: snapshot.SandboxConfig{
			SnapshotPolicy: sb.SnapshotPolicy,
			Image:          sb.Image, Network: sb.Network, Limits: sb.Limits,
			Env: maps.Clone(sb.Env), Labels: maps.Clone(sb.Labels), IdleTimeoutSeconds: int(sb.IdleTimeout.Seconds()),
		},
	}
	if manifest.Trigger == "" {
		manifest.Trigger = "manual"
	}
	if req.KeepRunning {
		manifest.Mode = "checkpoint"
	}
	if manifest.Trigger == "expiry" {
		deadline := sb.ExpiresAt.Add(s.snapshotCfg.Timeout)
		if sb.IdleTimeout > 0 && !s.busy(sb.ID) {
			idleDeadline := sb.LastActivityAt.Add(sb.IdleTimeout).Add(s.snapshotCfg.Timeout)
			if idleDeadline.Before(deadline) {
				deadline = idleDeadline
			}
		}
		if deadline.Before(manifest.DeadlineAt) {
			manifest.DeadlineAt = deadline
		}
	}
	// Establish cloud access before stopping the source sandbox.
	if err := s.snapshots.PutManifest(ctx, manifest); err != nil {
		return nil, snapshotStorageError(err)
	}
	_, err = s.repo.Update(ctx, id, func(record *model.Sandbox) error {
		if !canSnapshot(record) || req.KeepRunning && record.State != consts.SandboxStateRunning {
			return appctx.ErrSandboxNotRunning()
		}
		record.SnapshotInProgress = true
		if !req.KeepRunning {
			record.State = consts.SandboxStateSnapshotting
			record.StopReason = consts.StopReasonSnapshot
		}
		if manifest.Trigger == "expiry" {
			record.ExpiryBackupAttempted = true
		}
		record.FailureReason = ""
		return nil
	})
	if err != nil {
		manifest.Status, manifest.FailureReason = snapshot.Failed, "source sandbox changed before capture"
		_ = s.snapshots.PutManifest(ctx, manifest)
		return nil, mapError(err)
	}
	response := snapshotResponse(manifest, s.now())
	owned = false
	go func() {
		defer release()
		defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
		s.runSnapshot(manifest, file)
	}()
	return &response, nil
}

func canSnapshot(sb *model.Sandbox) bool {
	return !sb.SnapshotInProgress && (sb.State == consts.SandboxStateRunning || (sb.State == consts.SandboxStateFailed && sb.Active() && sb.StopReason == consts.StopReasonSnapshot))
}

func (s *service) runSnapshot(m snapshot.Manifest, file *os.File) {
	ctx, cancel := context.WithDeadline(context.Background(), m.DeadlineAt)
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	err := safego.Run(ctx, s.logger, func() error { return s.captureAndUpload(ctx, &m, file) })
	if err != nil {
		s.logger.Error("snapshot failed", zap.String("snapshot_id", m.ID), zap.Error(err))
		m.Status, m.FailureReason = snapshot.Failed, "snapshot failed; source filesystem retained for retry"
		// Persist failure even if the job deadline or shutdown canceled ctx.
		failureCtx, failureCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer failureCancel()
		_ = s.snapshots.PutManifest(failureCtx, m)
		_, _ = s.repo.Update(failureCtx, m.SandboxID, func(sb *model.Sandbox) error {
			sb.SnapshotInProgress = false
			sb.LastSnapshotError = "backup failed; check cloud storage and retry"
			sb.NextSnapshotAt = s.now().Add(time.Minute)
			if m.Mode != "checkpoint" || errors.Is(err, engine.ErrCheckpointThaw) {
				sb.State, sb.FailureReason = consts.SandboxStateFailed, m.FailureReason
				sb.StopReason = consts.StopReasonSnapshot
			}
			return nil // StoppedAt remains nil: retain capacity until actual cleanup.
		})
		return
	}
	if m.Mode == "checkpoint" {
		_, _ = s.repo.Update(ctx, m.SandboxID, func(sb *model.Sandbox) error {
			sb.SnapshotInProgress, sb.LastSnapshotError = false, ""
			sb.LastSnapshotAt = s.now()
			sb.NextSnapshotAt = nextSnapshotTime(s.now(), sb.SnapshotPolicy.IntervalSeconds)
			return nil
		})
		s.pruneAutomaticSnapshots(ctx, m)
		return
	}
	_, _ = s.repo.Update(ctx, m.SandboxID, func(sb *model.Sandbox) error {
		sb.SnapshotInProgress, sb.LastSnapshotError, sb.LastSnapshotAt = false, "", s.now()
		return nil
	})
	s.pruneAutomaticSnapshots(ctx, m)
	// Only a verified cloud backup permits destruction of the source filesystem.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), s.sandbox.DestroyTimeout)
	defer cleanupCancel()
	if err := s.engine.Destroy(cleanupCtx, m.SandboxID); err != nil {
		s.logger.Error("cleanup snapshotted sandbox", zap.String("sandbox_id", m.SandboxID), zap.Error(err))
		_, _ = s.repo.Update(cleanupCtx, m.SandboxID, func(sb *model.Sandbox) error {
			sb.State, sb.FailureReason = consts.SandboxStateFailed, "snapshot ready; sandbox cleanup failed; retry delete"
			return nil
		})
		return
	}
	s.markStopped(cleanupCtx, m.SandboxID, consts.SandboxStateStopped, consts.StopReasonSnapshot, "")
	s.metrics.SandboxStopped(m.Config.Network)
	s.metrics.SandboxEvent(consts.StopReasonSnapshot)
}

func (s *service) captureAndUpload(ctx context.Context, m *snapshot.Manifest, file *os.File) error {
	sum := sha256.New()
	capture := s.engine.CaptureSnapshot
	captureCtx := ctx
	if m.Mode == "checkpoint" {
		capture = s.engine.CaptureCheckpoint
		var cancel context.CancelFunc
		captureCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	if err := capture(captureCtx, m.SandboxID, io.MultiWriter(file, sum), s.snapshotCfg.MaxBytes); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	m.SizeBytes, m.SHA256, m.Status = info.Size(), hex.EncodeToString(sum.Sum(nil)), snapshot.Uploading
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := s.snapshots.PutManifest(ctx, *m); err != nil {
		return err
	}
	if err := s.snapshots.Upload(ctx, m.ID, file, m.SizeBytes, m.SHA256); err != nil {
		return err
	}
	// Verify the provider's checksum (or read back the archive when unavailable)
	// before committing the ready manifest and releasing the source filesystem.
	if verifier, ok := s.snapshots.(snapshot.Verifier); ok {
		err = verifier.Verify(ctx, *m, file)
	} else {
		err = s.snapshots.Download(ctx, *m, io.Discard)
	}
	if err != nil {
		return err
	}
	m.Status = snapshot.Ready
	return s.snapshots.PutManifest(ctx, *m)
}

func (s *service) GetSnapshot(ctx context.Context, id string) (*presentation.SnapshotResponse, error) {
	s.snapshotStoreMu.RLock()
	defer s.snapshotStoreMu.RUnlock()
	if err := s.snapshotEnabled(); err != nil {
		return nil, err
	}
	m, err := s.snapshots.GetManifest(ctx, id)
	if err != nil {
		return nil, snapshotStorageError(err)
	}
	response := snapshotResponse(m, s.now())
	return &response, nil
}

func (s *service) ListSnapshots(ctx context.Context) ([]presentation.SnapshotResponse, error) {
	s.snapshotStoreMu.RLock()
	defer s.snapshotStoreMu.RUnlock()
	if err := s.snapshotEnabled(); err != nil {
		return nil, err
	}
	items, err := s.snapshots.List(ctx)
	if err != nil {
		return nil, snapshotStorageError(err)
	}
	result := make([]presentation.SnapshotResponse, 0, len(items))
	for _, m := range items {
		result = append(result, snapshotResponse(m, s.now()))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (s *service) DeleteSnapshot(ctx context.Context, id string) error {
	s.snapshotStoreMu.RLock()
	defer s.snapshotStoreMu.RUnlock()
	if err := s.snapshotEnabled(); err != nil {
		return err
	}
	m, err := s.snapshots.GetManifest(ctx, id)
	if err != nil {
		return snapshotStorageError(err)
	}
	if status := m.EffectiveStatus(s.now()); status == snapshot.Capturing || status == snapshot.Uploading {
		return snapshotNotReady()
	}
	return snapshotStorageError(s.snapshots.Delete(ctx, id))
}

func snapshotResponse(m snapshot.Manifest, now time.Time) presentation.SnapshotResponse {
	status := m.EffectiveStatus(now)
	reason := m.FailureReason
	if status == snapshot.Failed && reason == "" {
		reason = "snapshot job interrupted or timed out"
	}
	return presentation.SnapshotResponse{
		Mode: m.Mode, Trigger: m.Trigger,
		ID: m.ID, SandboxID: m.SandboxID, Status: status,
		CreatedAt: m.CreatedAt, SizeBytes: m.SizeBytes, SHA256: m.SHA256, Architecture: m.Architecture,
		Labels: maps.Clone(m.Labels), FailureReason: reason,
	}
}

func snapshotNotReady() error {
	return appctx.NewHTTPError(http.StatusConflict, "snapshot is not ready").WithErrorCode(appctx.CodeSnapshotNotReady)
}

func snapshotStorageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, snapshot.ErrNotFound) || errors.Is(err, snapshot.ErrInvalidID) || errors.Is(err, snapshot.ErrCorrupt) || errors.Is(err, snapshot.ErrTooLarge) {
		return mapError(err)
	}
	return appctx.NewHTTPError(http.StatusServiceUnavailable, "snapshot storage unavailable").WithErrorCode(appctx.CodeSnapshotStorage).WithCause(err)
}

func (s *service) prepareRestore(ctx context.Context, req presentation.CreateSandboxRequest) (presentation.CreateSandboxRequest, string, func(), error) {
	release, err := s.reserveSnapshot()
	if err != nil {
		return req, "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.snapshotCfg.Timeout)
	var file *os.File
	cleanup := func() {
		cancel()
		if file != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
		release()
	}
	fail := func(err error) (presentation.CreateSandboxRequest, string, func(), error) {
		cleanup()
		return req, "", nil, err
	}
	if req.Image != "" {
		return fail(appctx.ErrLimitExceeded("image and snapshot_id are mutually exclusive"))
	}
	m, err := s.snapshots.GetManifest(ctx, req.SnapshotID)
	if err != nil {
		return fail(snapshotStorageError(err))
	}
	if m.EffectiveStatus(s.now()) != snapshot.Ready {
		return fail(snapshotNotReady())
	}
	if m.Version != snapshot.Version || m.Architecture != runtime.GOARCH {
		return fail(appctx.ErrLimitExceeded("snapshot format or architecture is incompatible with this host"))
	}
	if req.CPUMillis == 0 {
		req.CPUMillis = m.Config.Limits.CPUMillis
	}
	if req.MemoryMB == 0 {
		req.MemoryMB = m.Config.Limits.MemoryMB
	}
	if req.StorageMB == 0 {
		req.StorageMB = m.Config.Limits.StorageMB
	}
	if req.PidsMax == 0 {
		req.PidsMax = m.Config.Limits.PidsMax
	}
	if req.Network == "" {
		req.Network = m.Config.Network
	}
	if req.IdleTimeoutSeconds == 0 {
		req.IdleTimeoutSeconds = m.Config.IdleTimeoutSeconds
	}
	if req.Env == nil {
		req.Env = maps.Clone(m.Config.Env)
	} else {
		env := maps.Clone(m.Config.Env)
		if env == nil {
			env = map[string]string{}
		}
		maps.Copy(env, req.Env)
		req.Env = env
	}
	if req.Labels == nil {
		req.Labels = maps.Clone(m.Config.Labels)
	}
	if req.SnapshotPolicy == nil {
		policy := m.Config.SnapshotPolicy
		req.SnapshotPolicy = &policy
	}
	// Validate cloud-supplied defaults too, and enforce this host's limits
	// before downloading or extracting any archive.
	if err := validation.Struct(req); err != nil {
		return fail(appctx.ErrLimitExceeded("invalid restored sandbox configuration"))
	}
	if _, err := s.newSandbox(req); err != nil {
		return fail(err)
	}
	file, err = s.snapshotTemp()
	if err != nil {
		return fail(mapError(err))
	}
	if err := s.snapshots.Download(ctx, m, file); err != nil {
		return fail(snapshotStorageError(err))
	}
	// Image is informational on a restored sandbox; the engine uses the archive.
	req.Image, req.SnapshotID = m.Config.Image, ""
	return req, file.Name(), cleanup, nil
}
