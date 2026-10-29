//go:build linux

package fsops

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// newRoot builds a fake sandbox root next to a "host" secret outside it.
func newRoot(t *testing.T) (*Root, string, string) {
	t.Helper()

	base := t.TempDir()
	rootDir := filepath.Join(base, "rootfs")
	secret := filepath.Join(base, "host-secret")
	if err := os.MkdirAll(filepath.Join(rootDir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("HOST"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "etc", "shadow"), []byte("SANDBOX"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := OpenRoot(rootDir, os.Getuid(), os.Getgid())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, rootDir, secret
}

func readAll(t *testing.T, root *Root, p string) (string, error) {
	t.Helper()

	file, _, err := root.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	return string(data), err
}

func TestAbsoluteSymlinkResolvesInsideRoot(t *testing.T) {
	t.Parallel()

	root, rootDir, secret := newRoot(t)
	// A sandbox process plants a symlink to an absolute host path.
	if err := os.Symlink(secret, filepath.Join(rootDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", filepath.Join(rootDir, "shadow-link")); err != nil {
		t.Fatal(err)
	}

	if content, err := readAll(t, root, "/escape"); err == nil || content == "HOST" {
		t.Fatalf("absolute symlink escaped the root: content=%q err=%v", content, err)
	}
	content, err := readAll(t, root, "/shadow-link")
	if err != nil || content != "SANDBOX" {
		t.Fatalf("symlink to /etc/shadow = %q, %v; want the sandbox's own file", content, err)
	}
}

func TestDotDotStaysInsideRoot(t *testing.T) {
	t.Parallel()

	root, rootDir, _ := newRoot(t)
	if err := os.Symlink("../../../../host-secret", filepath.Join(rootDir, "up")); err != nil {
		t.Fatal(err)
	}

	if content, err := readAll(t, root, "/up"); err == nil || content == "HOST" {
		t.Fatalf("relative symlink escaped the root: content=%q err=%v", content, err)
	}
	if content, err := readAll(t, root, "/../host-secret"); err == nil || content == "HOST" {
		t.Fatalf("dot-dot path escaped the root: content=%q err=%v", content, err)
	}
}

func TestProcMagicLinkIsRejected(t *testing.T) {
	t.Parallel()

	root, rootDir, _ := newRoot(t)
	if err := os.Symlink("/proc/self/root/etc/hostname", filepath.Join(rootDir, "magic")); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, root, "/magic"); err == nil {
		t.Fatal("magic link through /proc resolved")
	}
}

func TestWriteFileDoesNotFollowSymlinkOutOfRoot(t *testing.T) {
	t.Parallel()

	root, rootDir, secret := newRoot(t)
	if err := os.Symlink(secret, filepath.Join(rootDir, "target")); err != nil {
		t.Fatal(err)
	}

	if _, err := root.WriteFile("/target", 0o644, strings.NewReader("pwned"), 1024); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	host, err := os.ReadFile(secret)
	if err != nil || string(host) != "HOST" {
		t.Fatalf("host file modified: %q, %v", host, err)
	}
	content, err := readAll(t, root, "/target")
	if err != nil || content != "pwned" {
		t.Fatalf("sandbox file = %q, %v", content, err)
	}
}

func TestWriteFileEnforcesLimitAtomically(t *testing.T) {
	t.Parallel()

	root, _, _ := newRoot(t)
	if _, err := root.WriteFile("/etc/shadow", 0o644, bytes.NewReader(make([]byte, 2048)), 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("WriteFile() error = %v, want ErrTooLarge", err)
	}
	content, err := readAll(t, root, "/etc/shadow")
	if err != nil || content != "SANDBOX" {
		t.Fatalf("oversized write replaced the file: %q, %v", content, err)
	}
	entries, err := root.ReadDir("/etc")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary file left behind: %+v", entries)
	}
}

func TestMkdirListStatRemove(t *testing.T) {
	t.Parallel()

	root, _, _ := newRoot(t)
	info, err := root.Mkdir("/a/b/c", 0o750, true)
	if err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if !info.Mode.IsDir() || info.Mode.Perm() != 0o750 {
		t.Fatalf("Mkdir() info = %+v", info)
	}
	if _, err := root.Mkdir("/a/b/c", 0o750, false); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("Mkdir() existing error = %v, want EEXIST", err)
	}
	if _, err := root.WriteFile("/a/b/c/file.txt", 0o600, strings.NewReader("x"), 10); err != nil {
		t.Fatal(err)
	}

	entries, err := root.ReadDir("/a/b/c")
	if err != nil || len(entries) != 1 || entries[0].Name != "file.txt" || entries[0].Size != 1 {
		t.Fatalf("ReadDir() = %+v, %v", entries, err)
	}
	if err := root.Remove("/a", false); !errors.Is(err, unix.ENOTEMPTY) {
		t.Fatalf("Remove() non-recursive error = %v, want ENOTEMPTY", err)
	}
	if err := root.Remove("/a", true); err != nil {
		t.Fatalf("Remove() recursive error = %v", err)
	}
	if _, err := root.Stat("/a"); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("Stat() after remove error = %v, want ENOENT", err)
	}
}

func TestRemoveDoesNotFollowSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	root, rootDir, _ := newRoot(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "dir", "link")); err != nil {
		t.Fatal(err)
	}

	if err := root.Remove("/dir", true); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("recursive remove followed a symlink out of the root: %v", err)
	}
}

func TestOpenRejectsFIFO(t *testing.T) {
	t.Parallel()

	root, rootDir, _ := newRoot(t)
	if err := unix.Mkfifo(filepath.Join(rootDir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := root.Open("/fifo"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Open(fifo) error = %v, want ErrNotRegular", err)
	}
}
