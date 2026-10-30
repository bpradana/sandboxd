package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

type memorySnapshots struct {
	mu          sync.Mutex
	manifests   map[string]snapshot.Manifest
	images      map[string]snapshot.ImageRef
	data        []byte
	uploadErr   error
	downloadErr error
	verified    bool
}

func (m *memorySnapshots) PutImageRef(_ context.Context, ref snapshot.ImageRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.images == nil {
		m.images = map[string]snapshot.ImageRef{}
	}
	m.images[ref.Name] = ref
	return nil
}

func (m *memorySnapshots) GetImageRef(_ context.Context, name string) (snapshot.ImageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.images[name]
	if !ok {
		return ref, snapshot.ErrNotFound
	}
	return ref, nil
}

func (m *memorySnapshots) ListImageRefs(context.Context) ([]snapshot.ImageRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := make([]snapshot.ImageRef, 0, len(m.images))
	for _, ref := range m.images {
		refs = append(refs, ref)
	}
	return refs, nil
}

func (m *memorySnapshots) DeleteImageRef(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.images, name)
	return nil
}

func (m *memorySnapshots) PutManifest(_ context.Context, item snapshot.Manifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.manifests[item.ID] = item
	return nil
}

func (m *memorySnapshots) GetManifest(_ context.Context, id string) (snapshot.Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, ok := m.manifests[id]
	if !ok {
		return item, snapshot.ErrNotFound
	}
	return item, nil
}

func (m *memorySnapshots) List(context.Context) ([]snapshot.Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []snapshot.Manifest{}
	for _, item := range m.manifests {
		result = append(result, item)
	}
	return result, nil
}

func (m *memorySnapshots) Upload(_ context.Context, _ string, src io.ReadSeeker, _ int64, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploadErr != nil {
		return m.uploadErr
	}
	var err error
	m.data, err = io.ReadAll(src)
	return err
}

func (m *memorySnapshots) Download(_ context.Context, _ snapshot.Manifest, dst io.Writer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.downloadErr != nil {
		return m.downloadErr
	}
	_, err := dst.Write(m.data)
	m.verified = err == nil
	return err
}

func (m *memorySnapshots) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.manifests, id)
	return nil
}

func snapshotFixture(t *testing.T) (*fixture, *memorySnapshots) {
	t.Helper()
	f := newFixture(t)
	f.now = time.Now()
	store := &memorySnapshots{manifests: map[string]snapshot.Manifest{}}
	f.svc.snapshots = store
	f.svc.snapshotCfg = config.SnapshotConfig{Enabled: true, TempDir: t.TempDir(), Timeout: time.Minute, MaxBytes: 1 << 20, MaxConcurrent: 1}
	f.svc.snapshotSlots = make(chan struct{}, 1)
	return f, store
}

func TestSnapshotVerifiesBeforeDestroy(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.running(t, "source")
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureSnapshot(gomock.Any(), "source", gomock.Any(), int64(1<<20)).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		_, err := dst.Write([]byte("filesystem archive"))
		return err
	})
	f.engine.EXPECT().Destroy(gomock.Any(), "source").DoAndReturn(func(context.Context, string) error {
		store.mu.Lock()
		defer store.mu.Unlock()
		if !store.verified {
			t.Error("source destroyed before cloud verification")
		}
		return nil
	})
	resp, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{Labels: map[string]string{"task": "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != snapshot.Capturing {
		t.Fatalf("initial status %s", resp.Status)
	}
	f.svc.snapshotJobs.Wait()
	ready, err := f.svc.GetSnapshot(context.Background(), resp.ID)
	if err != nil || ready.Status != snapshot.Ready || ready.SizeBytes != int64(len(store.data)) {
		t.Fatalf("snapshot %+v, error %v", ready, err)
	}
	digest := sha256.Sum256(store.data)
	if ready.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("incorrect archive checksum")
	}
	sb, _ := f.repo.Get(context.Background(), "source")
	if sb.Active() || sb.State != consts.SandboxStateStopped || sb.StopReason != consts.StopReasonSnapshot {
		t.Fatalf("source %+v", sb)
	}
	if entries, _ := os.ReadDir(f.svc.snapshotCfg.TempDir); len(entries) != 0 {
		t.Fatal("temporary archive leaked")
	}
}

func TestSnapshotFailureRetainsSourceAndCanRetry(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.running(t, "source")
	store.uploadErr = errors.New("cloud unavailable")
	f.engine.EXPECT().Ready().Return(nil).Times(2)
	f.engine.EXPECT().CaptureSnapshot(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		_, err := dst.Write([]byte("archive"))
		return err
	}).Times(2)
	first, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.snapshotJobs.Wait()
	sb, _ := f.repo.Get(context.Background(), "source")
	if !sb.Active() || sb.State != consts.SandboxStateFailed {
		t.Fatalf("failed source must retain its resource reservation: %+v", sb)
	}
	failed, _ := f.svc.GetSnapshot(context.Background(), first.ID)
	if failed.Status != snapshot.Failed {
		t.Fatalf("status %s", failed.Status)
	}
	store.uploadErr = nil
	f.engine.EXPECT().Destroy(gomock.Any(), "source").Return(nil)
	second, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.snapshotJobs.Wait()
	ready, _ := f.svc.GetSnapshot(context.Background(), second.ID)
	if ready.Status != snapshot.Ready {
		t.Fatalf("retry status %s", ready.Status)
	}
}

func TestSnapshotRejectsConcurrentDeleteAndBoundsJobs(t *testing.T) {
	t.Parallel()
	f, _ := snapshotFixture(t)
	f.running(t, "source")
	started, proceed := make(chan struct{}), make(chan struct{})
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureSnapshot(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		close(started)
		<-proceed
		_, err := dst.Write([]byte("archive"))
		return err
	})
	f.engine.EXPECT().Destroy(gomock.Any(), "source").Return(nil)
	resp, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := f.svc.Destroy(context.Background(), "source"); httpStatus(err) != http.StatusConflict {
		t.Errorf("delete during capture: %v", err)
	}
	if err := f.svc.DeleteSnapshot(context.Background(), resp.ID); httpStatus(err) != http.StatusConflict {
		t.Errorf("delete active snapshot: %v", err)
	}
	if _, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{}); httpStatus(err) != http.StatusTooManyRequests {
		t.Errorf("unbounded snapshots: %v", err)
	}
	close(proceed)
	f.svc.snapshotJobs.Wait()
}

func TestRestoreInheritsConfigurationAndFreshTTL(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	id := "snap-0123456789abcdef01234567"
	store.data = []byte("archive")
	store.manifests[id] = snapshot.Manifest{
		ID: id, Version: snapshot.Version, Status: snapshot.Ready, Architecture: runtime.GOARCH,
		Config: snapshot.SandboxConfig{Image: "image-not-on-this-host", Network: "none", Limits: testLimits(), Env: map[string]string{"SAVED": "yes", "OVERRIDE": "old"}, Labels: map[string]string{"task": "saved"}},
	}
	f.engine.EXPECT().Ready().Return(nil)
	var archivePath string
	f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, spec engine.Spec) (*engine.Info, error) {
		archivePath = spec.SnapshotArchive
		data, err := os.ReadFile(archivePath)
		if err != nil || string(data) != "archive" || spec.Limits.MemoryMB != 512 || spec.Env["SAVED"] != "yes" || spec.Env["OVERRIDE"] != "new" {
			t.Errorf("restored spec %+v, archive %q, error %v", spec, data, err)
		}
		return &engine.Info{}, nil
	})
	resp, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{SnapshotID: id, MemoryMB: 512, TTLSeconds: 120, Env: map[string]string{"OVERRIDE": "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.ExpiresAt.Equal(f.now.Add(120*time.Second)) || resp.Labels["task"] != "saved" {
		t.Fatalf("restored response %+v", resp)
	}
	if _, err := os.Stat(archivePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore temp archive leaked")
	}
}

func testLimits() model.Limits {
	return model.Limits{CPUMillis: 1000, MemoryMB: 256, StorageMB: 128, PidsMax: 64}
}

func TestRestoreRejectsCorruptionAndIncompatibleSnapshots(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"corrupt", "architecture", "not ready", "limits"} {
		t.Run(scenario, func(t *testing.T) {
			f, store := snapshotFixture(t)
			id := "snap-0123456789abcdef01234567"
			m := snapshot.Manifest{ID: id, Version: snapshot.Version, Status: snapshot.Ready, Architecture: runtime.GOARCH, Config: snapshot.SandboxConfig{Network: "none", Limits: testLimits()}}
			switch scenario {
			case "corrupt":
				store.downloadErr = snapshot.ErrCorrupt
			case "architecture":
				m.Architecture = "other"
			case "not ready":
				m.Status = snapshot.Failed
			case "limits":
				m.Config.Limits.MemoryMB = 99999
			}
			store.manifests[id] = m
			f.engine.EXPECT().Ready().Return(nil)
			if _, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{SnapshotID: id}); err == nil {
				t.Fatal("invalid restore accepted")
			}
			if entries, _ := os.ReadDir(f.svc.snapshotCfg.TempDir); len(entries) != 0 {
				t.Fatal("failed restore leaked temporary file")
			}
		})
	}
}

func TestSnapshotsDisabledByDefault(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{}); httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("disabled snapshots: %v", err)
	}
}

func TestSnapshotShutdownCancelsWork(t *testing.T) {
	t.Parallel()
	f, _ := snapshotFixture(t)
	f.running(t, "source")
	started := make(chan struct{})
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureSnapshot(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ string, _ io.Writer, _ int64) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if _, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{}); err != nil {
		t.Fatal(err)
	}
	<-started
	close(f.svc.stop)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := f.svc.stopSnapshots(ctx); err != nil {
		t.Fatalf("shutdown did not drain jobs: %v", err)
	}
	if _, err := f.svc.reserveSnapshot(); httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("shutdown accepted new work: %v", err)
	}
}

func TestSnapshotStartupCleansOnlyScratchArchives(t *testing.T) {
	t.Parallel()
	f, _ := snapshotFixture(t)
	for _, name := range []string{"snapshot-abandoned.tar.gz", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(f.svc.snapshotCfg.TempDir, name), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.svc.cleanupSnapshotTemps(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(f.svc.snapshotCfg.TempDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "keep.txt" {
		t.Fatalf("scratch cleanup: %+v %v", entries, err)
	}
}
