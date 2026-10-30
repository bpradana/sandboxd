package sandbox

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func TestScheduledSnapshotKeepsRunningAndRetainsOnlyAutomaticBackups(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.running(t, "source")
	ctx := context.Background()
	_, err := f.svc.SetSnapshotPolicy(ctx, "source", model.SnapshotPolicy{IntervalSeconds: 60, BeforeExpiry: true, Retain: 1})
	if err != nil {
		t.Fatal(err)
	}
	store.manifests["manual"] = snapshot.Manifest{ID: "manual", SandboxID: "source", Status: snapshot.Ready, Trigger: "manual", CreatedAt: f.now.Add(-time.Hour)}
	store.manifests["old"] = snapshot.Manifest{ID: "old", SandboxID: "source", Status: snapshot.Ready, Trigger: "scheduled", CreatedAt: f.now.Add(-time.Minute)}
	f.now = f.now.Add(time.Minute)
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureCheckpoint(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		_, err := dst.Write([]byte("checkpoint"))
		return err
	})
	f.svc.reapOnce(ctx)
	f.svc.snapshotJobs.Wait()
	sb, _ := f.repo.Get(ctx, "source")
	if sb.State != consts.SandboxStateRunning || sb.SnapshotInProgress || !sb.LastSnapshotAt.Equal(f.now) || !sb.NextSnapshotAt.Equal(f.now.Add(time.Minute)) {
		t.Fatalf("checkpoint state %+v", sb)
	}
	if _, ok := store.manifests["old"]; ok {
		t.Fatal("old automatic backup retained")
	}
	if _, ok := store.manifests["manual"]; !ok {
		t.Fatal("manual backup deleted")
	}
	if len(store.manifests) != 2 {
		t.Fatalf("retained %d manifests", len(store.manifests))
	}
}

func TestCheckpointFailureKeepsSourceRunningAndRetriesLater(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.running(t, "source")
	store.uploadErr = errors.New("offline")
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureCheckpoint(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		_, err := dst.Write([]byte("checkpoint"))
		return err
	})
	_, err := f.svc.CreateSnapshot(context.Background(), "source", presentation.CreateSnapshotRequest{KeepRunning: true})
	if err != nil {
		t.Fatal(err)
	}
	f.svc.snapshotJobs.Wait()
	sb, _ := f.repo.Get(context.Background(), "source")
	if sb.State != consts.SandboxStateRunning || sb.SnapshotInProgress || sb.LastSnapshotError == "" || !sb.NextSnapshotAt.After(f.now) {
		t.Fatalf("failure state %+v", sb)
	}
}

func TestExpirySnapshotRunsBeforeTeardown(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.running(t, "source")
	ctx := context.Background()
	_, err := f.svc.SetSnapshotPolicy(ctx, "source", model.SnapshotPolicy{BeforeExpiry: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.repo.Update(ctx, "source", func(sb *model.Sandbox) error { sb.IdleTimeout = 0; return nil })
	f.now = f.now.Add(10*time.Minute + time.Second)
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().CaptureSnapshot(gomock.Any(), "source", gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, _ string, dst io.Writer, _ int64) error {
		_, err := dst.Write([]byte("final filesystem"))
		return err
	})
	f.engine.EXPECT().Destroy(gomock.Any(), "source").Return(nil)
	f.svc.reapOnce(ctx)
	f.svc.snapshotJobs.Wait()
	sb, _ := f.repo.Get(ctx, "source")
	if sb.State != consts.SandboxStateStopped || sb.Active() {
		t.Fatalf("expiry state %+v", sb)
	}
	for _, m := range store.manifests {
		if m.Trigger != "expiry" || m.Status != snapshot.Ready {
			t.Fatalf("final backup %+v", m)
		}
	}
	if len(store.manifests) != 1 {
		t.Fatal("missing final backup")
	}
}

func TestPolicyValidationAndExpiredBackupGrace(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.running(t, "source")
	ctx := context.Background()
	if _, err := f.svc.SetSnapshotPolicy(ctx, "source", model.SnapshotPolicy{IntervalSeconds: 1}); httpStatus(err) != 400 {
		t.Fatalf("short interval accepted: %v", err)
	}
	if _, err := f.svc.SetSnapshotPolicy(ctx, "source", model.SnapshotPolicy{BeforeExpiry: true}); httpStatus(err) != 503 {
		t.Fatalf("disabled storage accepted: %v", err)
	}
	f.svc.snapshotCfg.Timeout = time.Minute
	_, _ = f.repo.Update(ctx, "source", func(sb *model.Sandbox) error {
		sb.SnapshotPolicy.BeforeExpiry = true
		sb.ExpiresAt = f.now.Add(-2 * time.Minute)
		return nil
	})
	f.engine.EXPECT().Destroy(gomock.Any(), "source").Return(nil)
	f.svc.reapOnce(ctx)
	sb, _ := f.repo.Get(ctx, "source")
	if sb.Active() {
		t.Fatal("backup failure extended expiry without bound")
	}
}
