package sandbox

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func TestPromotionPublishesTheImageForOtherHosts(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", []byte("archive"))
	f.engine.EXPECT().ImportImage(gomock.Any(), "python-ready", gomock.Any(), gomock.Any()).
		Return(&engine.Image{Name: "python-ready", CreatedAt: time.Now()}, nil)

	if _, err := f.svc.PromoteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa",
		presentation.PromoteSnapshotRequest{Image: "python-ready"}); err != nil {
		t.Fatalf("PromoteSnapshot: %v", err)
	}

	ref, err := store.GetImageRef(context.Background(), "python-ready")
	if err != nil {
		t.Fatalf("a promoted image must be findable by other hosts: %v", err)
	}
	if ref.SnapshotID != "snap-aaaaaaaaaaaaaaaaaaaaaaaa" || ref.Architecture != runtime.GOARCH {
		t.Errorf("the reference does not point back at the archive: %+v", ref)
	}
}

func TestCreateFetchesAnImageThisHostHasNeverHeld(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	archive := []byte("filesystem archive")
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", archive)
	if err := store.PutImageRef(context.Background(), snapshot.ImageRef{
		Name: "python-ready", SnapshotID: "snap-aaaaaaaaaaaaaaaaaaaaaaaa", Architecture: runtime.GOARCH,
	}); err != nil {
		t.Fatal(err)
	}

	gomock.InOrder(
		f.engine.EXPECT().Ready().Return(nil),
		// The host does not have it...
		f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, engine.ErrImageNotFound),
		// ...so it is built from the archive the promotion pointed at...
		f.engine.EXPECT().ImportImage(gomock.Any(), "python-ready", gomock.Any(), gomock.Any()).
			DoAndReturn(func(_ context.Context, name string, src io.Reader, _ int64) (*engine.Image, error) {
				got, err := io.ReadAll(src)
				if err != nil || string(got) != string(archive) {
					t.Errorf("imported %q (%v)", got, err)
				}
				return &engine.Image{Name: name}, nil
			}),
		// ...and the sandbox it was wanted for is built after all.
		f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).Return(&engine.Info{IPAddress: "10.88.0.2"}, nil),
	)

	sb, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{Image: "python-ready"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sb.Image != "python-ready" || sb.State != "running" {
		t.Errorf("sandbox came back as %+v", sb)
	}
	if sb.Node != "test-node" {
		t.Errorf("a sandbox should say which daemon it lives on, got %q", sb.Node)
	}
}

func TestCreateGivesUpWhenNoHostCanBuildTheImage(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", []byte("archive"))
	if err := store.PutImageRef(context.Background(), snapshot.ImageRef{
		Name: "elsewhere", SnapshotID: "snap-aaaaaaaaaaaaaaaaaaaaaaaa", Architecture: "an-architecture-this-host-is-not",
	}); err != nil {
		t.Fatal(err)
	}
	f.engine.EXPECT().Ready().Return(nil)
	// One attempt only: an image promoted elsewhere cannot be built here, so
	// there is nothing to retry.
	f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, engine.ErrImageNotFound)

	if _, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{Image: "elsewhere"}); err == nil {
		t.Fatal("a sandbox was created from an image that does not exist here")
	}
}

func TestImageListMarksWhatIsAlreadyHere(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	f.engine.EXPECT().Images(gomock.Any()).Return([]engine.Image{{Name: "alpine"}}, nil)
	for _, ref := range []snapshot.ImageRef{
		{Name: "alpine", SnapshotID: "snap-aaaaaaaaaaaaaaaaaaaaaaaa", Architecture: runtime.GOARCH},
		{Name: "python-ready", SnapshotID: "snap-bbbbbbbbbbbbbbbbbbbbbbbb", Architecture: runtime.GOARCH},
	} {
		if err := store.PutImageRef(context.Background(), ref); err != nil {
			t.Fatal(err)
		}
	}

	images, err := f.svc.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	found := map[string]presentation.ImageResponse{}
	for _, image := range images {
		found[image.Name] = image
	}
	if len(found) != 2 {
		t.Fatalf("an image held here and promoted should be listed once: %+v", images)
	}
	if !found["alpine"].Local {
		t.Error("alpine is on this host")
	}
	if found["python-ready"].Local || found["python-ready"].Architecture != runtime.GOARCH {
		t.Errorf("python-ready is only in cloud storage: %+v", found["python-ready"])
	}
}

func TestDeletingASnapshotAnImageIsBuiltOnIsRefused(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	readyManifest(t, store, "snap-aaaaaaaaaaaaaaaaaaaaaaaa", []byte("archive"))
	if err := store.PutImageRef(context.Background(), snapshot.ImageRef{
		Name: "python-ready", SnapshotID: "snap-aaaaaaaaaaaaaaaaaaaaaaaa", Architecture: runtime.GOARCH,
	}); err != nil {
		t.Fatal(err)
	}

	err := f.svc.DeleteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa")
	if httpStatus(err) != http.StatusConflict {
		t.Fatalf("deleting the archive under an image should be a conflict, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "python-ready") {
		t.Errorf("the refusal should name the image: %v", err)
	}

	// Removing the image frees the archive.
	f.engine.EXPECT().RemoveImage(gomock.Any(), "python-ready").Return(nil)
	if err := f.svc.RemoveImage(context.Background(), "python-ready"); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if err := f.svc.DeleteSnapshot(context.Background(), "snap-aaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("the archive should be deletable once nothing is built on it: %v", err)
	}
}

func TestRemoveImageForgetsAnImageThisHostNeverHeld(t *testing.T) {
	t.Parallel()
	f, store := snapshotFixture(t)
	if err := store.PutImageRef(context.Background(), snapshot.ImageRef{
		Name: "python-ready", SnapshotID: "snap-aaaaaaaaaaaaaaaaaaaaaaaa", Architecture: runtime.GOARCH,
	}); err != nil {
		t.Fatal(err)
	}
	// Not materialized here, but still the cluster's to forget.
	f.engine.EXPECT().RemoveImage(gomock.Any(), "python-ready").Return(engine.ErrImageNotFound)
	if err := f.svc.RemoveImage(context.Background(), "python-ready"); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if _, err := store.GetImageRef(context.Background(), "python-ready"); err == nil {
		t.Error("the cluster reference outlived the image")
	}
}
