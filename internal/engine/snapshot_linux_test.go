//go:build linux

package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/bpradana/sandboxd/internal/engine/image"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

func TestSnapshotFilesystemRoundTrip(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("uid mapping test requires root")
	}
	source := t.TempDir()
	for _, dir := range []string{"workspace", "proc", "sys", "dev", "run"} {
		if err := os.Mkdir(filepath.Join(source, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(source, "workspace", "data")
	if err := os.WriteFile(file, []byte("saved work"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(file, 101000, 101000); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, filepath.Join(source, "workspace", "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/workspace/data", filepath.Join(source, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(source, "workspace", "external")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "proc", "host-data"), []byte("exclude"), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := exportFilesystem(context.Background(), source, &archive, 100000, 65536, 1<<20); err != nil {
		t.Fatal(err)
	}
	store := image.NewStore(t.TempDir(), 200000, 65536)
	if err := store.ImportSnapshot(context.Background(), "restored", bytes.NewReader(archive.Bytes()), 1<<20); err != nil {
		t.Fatal(err)
	}
	root, err := store.Path("restored")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "workspace", "data"))
	if err != nil || string(data) != "saved work" {
		t.Fatalf("restored %q error %v", data, err)
	}
	info, err := os.Stat(filepath.Join(root, "workspace", "data"))
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != 201000 || info.Mode().Perm() != 0o640 {
		t.Fatalf("ownership/mode %d %o", st.Uid, info.Mode().Perm())
	}
	other, err := os.Stat(filepath.Join(root, "workspace", "hardlink"))
	if err != nil || !os.SameFile(info, other) {
		t.Fatal("hardlink not preserved")
	}
	target, err := os.Readlink(filepath.Join(root, "workspace", "link"))
	if err != nil || target != "/workspace/data" {
		t.Fatalf("symlink %q error %v", target, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "proc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime filesystem included")
	}
	if err := store.ImportSnapshot(context.Background(), "too-large", bytes.NewReader(archive.Bytes()), 512); err == nil {
		t.Fatal("unbounded archive expansion")
	}
	if _, err := store.Path("too-large"); !errors.Is(err, image.ErrNotFound) {
		t.Fatal("failed import published")
	}
}

func TestSnapshotCaptureLimitsAndCancellation(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "data"), bytes.Repeat([]byte("x"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := exportFilesystem(context.Background(), source, &archive, 100000, 65536, 1024); !errors.Is(err, snapshot.ErrTooLarge) {
		t.Fatalf("archive limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := exportFilesystem(ctx, source, &archive, 100000, 65536, 1<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
