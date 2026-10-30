package sandbox

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func TestChangesDescribesEveryKindAndLeavesDeletionsBare(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.running(t, "sbx-1")
	modified := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	f.engine.EXPECT().Changes(gomock.Any(), "sbx-1").Return(&engine.ChangeSet{
		Entries: []engine.Change{
			{Path: "/etc/hosts", Kind: engine.ChangeModified, Type: "file", Size: 64, Mode: 0o644, ModTime: modified, Seeded: true},
			{Path: "/root/out", Kind: engine.ChangeAdded, Type: "dir", Mode: 0o755, ModTime: modified},
			{Path: "/root/old.txt", Kind: engine.ChangeDeleted},
			{Path: "/vendor", Kind: engine.ChangeReplaced, Type: "dir", Mode: 0o755, ModTime: modified},
		},
		Truncated: true,
	}, nil)

	resp, err := f.svc.Changes(context.Background(), "sbx-1")
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if !resp.Truncated {
		t.Error("a truncated walk must stay truncated through the service")
	}
	byPath := map[string]presentation.SandboxChangeResponse{}
	for _, entry := range resp.Entries {
		byPath[entry.Path] = entry
	}
	if got := byPath["/etc/hosts"]; got.Kind != engine.ChangeModified || got.Mode != "0644" || !got.Seeded {
		t.Errorf("/etc/hosts came through as %+v", got)
	}
	if got := byPath["/etc/hosts"]; got.ModifiedAt == nil || !got.ModifiedAt.Equal(modified) {
		t.Errorf("/etc/hosts lost its timestamp: %+v", got.ModifiedAt)
	}
	if got := byPath["/vendor"]; got.Kind != engine.ChangeReplaced {
		t.Errorf("an emptied directory should say replaced, got %q", got.Kind)
	}
	// Nothing is left of a deleted path, so nothing may be claimed about it.
	deleted := byPath["/root/old.txt"]
	if deleted.Mode != "" || deleted.ModifiedAt != nil || deleted.Type != "" {
		t.Errorf("a deletion should carry no mode, type or time: %+v", deleted)
	}
}

func TestChangesNeedsARunningSandbox(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.svc.Changes(context.Background(), "missing"); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("an unknown sandbox should be 404, got %v", err)
	}
}

// readyManifest stores an archive that promotion can legitimately import.
func readyManifest(t *testing.T, store *memorySnapshots, id string, archive []byte) {
	t.Helper()
	store.data = archive
	if err := store.PutManifest(context.Background(), snapshot.Manifest{
		Version:      snapshot.Version,
		ID:           id,
		SandboxID:    "source",
		Status:       snapshot.Ready,
		Architecture: runtime.GOARCH,
		SizeBytes:    int64(len(archive)),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteSnapshotImportsTheArchiveUnderTheGivenName(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	archive := []byte("filesystem archive")
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", archive)

	created := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	f.engine.EXPECT().ImportImage(gomock.Any(), "python-ready", gomock.Any(), int64(1<<20)).
		DoAndReturn(func(_ context.Context, name string, src io.Reader, _ int64) (*engine.Image, error) {
			// The archive must arrive from its start, not from where the
			// download left the file.
			got, err := io.ReadAll(src)
			if err != nil {
				return nil, err
			}
			if string(got) != string(archive) {
				t.Errorf("imported %q, want %q", got, archive)
			}
			return &engine.Image{Name: name, CreatedAt: created}, nil
		})

	resp, err := f.svc.PromoteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa",
		presentation.PromoteSnapshotRequest{Image: "python-ready"})
	if err != nil {
		t.Fatalf("PromoteSnapshot: %v", err)
	}
	if resp.Name != "python-ready" || !resp.CreatedAt.Equal(created) {
		t.Errorf("promotion returned %+v", resp)
	}
}

func TestPromoteSnapshotRefusesBeforeItDownloadsAnything(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", []byte("archive"))

	// An unusable name is refused without a manifest read or a download; the
	// mock engine would fail the test if ImportImage were reached.
	for _, name := range []string{"", "Python", "-leading", "a/b"} {
		_, err := f.svc.PromoteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa",
			presentation.PromoteSnapshotRequest{Image: name})
		if err == nil {
			t.Fatalf("%q was accepted as an image name", name)
		}
		if status := httpStatus(err); status != http.StatusBadRequest {
			t.Errorf("%q was refused with %d", name, status)
		}
	}
}

func TestPromoteSnapshotChecksTheArchiveBelongsHere(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	if err := store.PutManifest(context.Background(), snapshot.Manifest{
		Version: snapshot.Version, ID: "snap-bbbbbbbbbbbbbbbbbbbbbbbb", Status: snapshot.Ready,
		Architecture: "an-architecture-this-host-is-not",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PromoteSnapshot(context.Background(), "snap-bbbbbbbbbbbbbbbbbbbbbbbb",
		presentation.PromoteSnapshotRequest{Image: "elsewhere"}); httpStatus(err) != http.StatusBadRequest {
		t.Fatalf("a foreign architecture should be refused, got %v", err)
	}

	if err := store.PutManifest(context.Background(), snapshot.Manifest{
		Version: snapshot.Version, ID: "snap-cccccccccccccccccccccccc", Status: snapshot.Capturing,
		Architecture: runtime.GOARCH, DeadlineAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PromoteSnapshot(context.Background(), "snap-cccccccccccccccccccccccc",
		presentation.PromoteSnapshotRequest{Image: "half-done"}); err == nil {
		t.Fatal("a snapshot still being captured is not something to import")
	}

	if _, err := f.svc.PromoteSnapshot(context.Background(), "snap-dddddddddddddddddddddddd",
		presentation.PromoteSnapshotRequest{Image: "missing"}); httpStatus(err) != http.StatusNotFound {
		t.Fatal("an unknown snapshot should be 404")
	}
}

func TestPromoteSnapshotNeedsSnapshotsEnabled(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if _, err := f.svc.PromoteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa",
		presentation.PromoteSnapshotRequest{Image: "nowhere"}); httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("without a snapshot store there is nothing to promote, got %v", err)
	}
}
