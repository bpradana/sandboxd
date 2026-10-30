//go:build linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// layer builds a directory tree from a map of path to contents; a nil value
// makes a directory instead of a file.
func layer(t *testing.T, root string, tree map[string]*string) {
	t.Helper()
	for name, content := range tree {
		full := filepath.Join(root, name)
		if content == nil {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", name, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir parent of %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(*content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func text(s string) *string { return &s }

// deviceCapableDir returns a scratch directory on a filesystem that accepts
// device nodes and trusted xattrs, which whiteouts and opaque markers are.
// A container's own overlayfs accepts neither, but the daemon's state
// directory must: it is where every sandbox's upper layer lives.
func deviceCapableDir(t *testing.T) string {
	t.Helper()
	candidates := []string{t.TempDir()}
	state := os.Getenv("SANDBOX_STATE_DIR")
	if state == "" {
		state = "/var/lib/sandboxd"
	}
	if dir, err := os.MkdirTemp(state, "changes-"); err == nil {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		candidates = append(candidates, dir)
	}
	for _, dir := range candidates {
		probe := filepath.Join(dir, ".probe")
		if unix.Mknod(probe, unix.S_IFCHR, 0) != nil {
			continue
		}
		err := unix.Setxattr(dir, opaqueXattr, []byte("y"), 0)
		_ = os.Remove(probe)
		if err == nil {
			_ = unix.Removexattr(dir, opaqueXattr)
			return dir
		}
	}
	t.Skip("no filesystem here accepts device nodes and trusted xattrs")
	return ""
}

func byPath(t *testing.T, set *ChangeSet) map[string]Change {
	t.Helper()
	found := make(map[string]Change, len(set.Entries))
	for _, change := range set.Entries {
		found[change.Path] = change
	}
	return found
}

func TestReadChangesClassifiesTheUpperLayer(t *testing.T) {
	dir := deviceCapableDir(t)
	upper, lower := filepath.Join(dir, "upper"), filepath.Join(dir, "lower")
	layer(t, lower, map[string]*string{
		"app.py":      text("from the image\n"),
		"gone.txt":    text("deleted later\n"),
		"etc/passwd":  text("root:x:0:0:::\n"),
		"vendor/lib":  text("shipped\n"),
		"untouched.c": text("never read\n"),
	})
	layer(t, upper, map[string]*string{
		"app.py":        text("edited by the workload\n"),
		"new.txt":       text("created by the workload\n"),
		"etc/hostname":  text("sandbox\n"),
		"vendor":        nil,
		"proc":          nil,
		"out/report.md": text("nested and new\n"),
	})

	// A whiteout is a character device numbered 0. Overlayfs can mark both
	// replacement directories and entirely new directories opaque; only the
	// former should be called replaced.
	if err := unix.Mknod(filepath.Join(upper, "gone.txt"), unix.S_IFCHR, 0); err != nil {
		t.Fatalf("create whiteout: %v", err)
	}
	for _, path := range []string{"vendor", "out"} {
		if err := unix.Setxattr(filepath.Join(upper, path), opaqueXattr, []byte("y"), 0); err != nil {
			t.Fatalf("mark %s opaque: %v", path, err)
		}
	}

	set, err := readChanges(context.Background(), upper, lower)
	if err != nil {
		t.Fatalf("readChanges: %v", err)
	}
	if set.Truncated {
		t.Fatal("a six-entry tree should not truncate")
	}
	found := byPath(t, set)

	for path, kind := range map[string]string{
		"/app.py":        ChangeModified,
		"/new.txt":       ChangeAdded,
		"/etc/hostname":  ChangeAdded,
		"/gone.txt":      ChangeDeleted,
		"/vendor":        ChangeReplaced,
		"/out":           ChangeAdded,
		"/out/report.md": ChangeAdded,
	} {
		change, ok := found[path]
		if !ok {
			t.Fatalf("%s is missing from the change list", path)
		}
		if change.Kind != kind {
			t.Errorf("%s: kind = %q, want %q", path, change.Kind, kind)
		}
	}

	// A directory the image already had is only a parent of what changed.
	if _, ok := found["/etc"]; ok {
		t.Error("/etc existed in the image and was only copied up as a parent")
	}
	// Mount points are the daemon's plumbing, not the workload's work.
	if _, ok := found["/proc"]; ok {
		t.Error("/proc is mounted over and should never be reported")
	}
	// Nothing the workload did not touch may appear.
	if _, ok := found["/untouched.c"]; ok {
		t.Error("a file only in the image is not a change")
	}

	if !found["/etc/hostname"].Seeded {
		t.Error("/etc/hostname is written into every sandbox and should say so")
	}
	if found["/new.txt"].Seeded {
		t.Error("a workload file is not seeded")
	}
	if found["/new.txt"].Type != "file" || found["/out"].Type != "dir" {
		t.Errorf("types are wrong: %q and %q", found["/new.txt"].Type, found["/out"].Type)
	}
	if size := found["/new.txt"].Size; size != int64(len("created by the workload\n")) {
		t.Errorf("/new.txt size = %d", size)
	}
	if found["/gone.txt"].Size != 0 || !found["/gone.txt"].ModTime.IsZero() {
		t.Error("a deletion leaves nothing to describe")
	}
}

func TestReadChangesWithoutAnImageCallsEverythingNew(t *testing.T) {
	dir := t.TempDir()
	upper := filepath.Join(dir, "upper")
	layer(t, upper, map[string]*string{"app.py": text("hello\n")})

	set, err := readChanges(context.Background(), upper, "")
	if err != nil {
		t.Fatalf("readChanges: %v", err)
	}
	found := byPath(t, set)
	if found["/app.py"].Kind != ChangeAdded {
		t.Errorf("without a lower layer every path is added, got %q", found["/app.py"].Kind)
	}
}

func TestReadChangesStopsAtTheLimitAndOnCancellation(t *testing.T) {
	dir := t.TempDir()
	upper := filepath.Join(dir, "upper")
	tree := make(map[string]*string, maxChanges+16)
	for i := range maxChanges + 16 {
		tree[filepath.Join("files", "f"+itoa(i))] = text("x")
	}
	layer(t, upper, tree)

	set, err := readChanges(context.Background(), upper, "")
	if err != nil {
		t.Fatalf("readChanges: %v", err)
	}
	if !set.Truncated {
		t.Fatal("a tree past the limit must report itself truncated")
	}
	if len(set.Entries) != maxChanges {
		t.Fatalf("kept %d entries, want %d", len(set.Entries), maxChanges)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readChanges(ctx, upper, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled walk should return its cancellation, got %v", err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
