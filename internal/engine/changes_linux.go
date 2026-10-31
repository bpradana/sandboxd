//go:build linux

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// A sandbox's root is an overlay of a writable layer over a read-only image,
// so the writable layer already holds the answer to "what did this workload
// touch?" and no tree has to be compared to find it. This reads that layer:
// whiteout device nodes are deletions, opaque directories are replacements,
// and everything else was added or modified depending on whether the image
// had the path underneath.

const (
	// maxChanges bounds the walk. Installing a large package tree copies up
	// tens of thousands of files, and no caller reads a list that long.
	maxChanges = 20000
	// opaqueXattr marks a directory whose lower entries must not show
	// through. Overlayfs uses it for replacements and can also set it on
	// newly created directories. Overlays mounted with userxattr record it
	// in the user namespace instead.
	opaqueXattr     = "trusted.overlay.opaque"
	opaqueUserXattr = "user.overlay.opaque"
)

// errTruncated stops the walk at maxChanges without losing what it collected.
var errTruncated = errors.New("change list truncated")

// mountPoints hold nothing of the workload's: init mounts over them, so their
// upper-layer entries describe the daemon's plumbing. CaptureSnapshot skips
// the same four.
var mountPoints = map[string]bool{"proc": true, "sys": true, "dev": true, "run": true}

// seededPaths are written into every sandbox before its first process runs.
// They are reported like any other change, with Seeded set, because the
// workload can edit them too and dropping them would hide that it had.
var seededPaths = map[string]bool{
	"/proc": true, "/sys": true, "/dev": true, "/etc": true, "/tmp": true,
	"/home": true, "/root": true, sandboxHome: true,
	"/etc/hostname": true, "/etc/hosts": true, "/etc/resolv.conf": true,
	"/etc/passwd": true, "/etc/group": true,
}

func (e *linuxEngine) Changes(ctx context.Context, id string) (*ChangeSet, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	// Read alongside the file API rather than against it: this only reads,
	// and a capture still drains both before it freezes.
	inst.fsMu.RLock()
	defer inst.fsMu.RUnlock()
	return readChanges(ctx, filepath.Join(inst.scratchDir(), "upper"), inst.lower)
}

func readChanges(ctx context.Context, upper, lower string) (*ChangeSet, error) {
	upperFD, err := unix.Open(upper, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(upperFD) }()

	walker := &changeWalker{ctx: ctx, lowerFD: -1}
	if lower != "" {
		// O_PATH is enough to stat through, and the image is never written.
		if fd, openErr := unix.Open(lower, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); openErr == nil {
			walker.lowerFD = fd
			defer func() { _ = unix.Close(fd) }()
		}
	}

	if err := walker.walk(upperFD, ""); err != nil && !errors.Is(err, errTruncated) {
		return nil, err
	}
	sort.Slice(walker.entries, func(i, j int) bool { return walker.entries[i].Path < walker.entries[j].Path })
	return &ChangeSet{Entries: walker.entries, Truncated: walker.truncated}, nil
}

type changeWalker struct {
	ctx     context.Context
	lowerFD int

	entries   []Change
	truncated bool
}

// walk reads one directory of the upper layer. dirfd stays the caller's to
// close, and rel is the directory's path inside the sandbox ("" is the root).
func (w *changeWalker) walk(dirfd int, rel string) error {
	names, err := readNames(dirfd)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		if rel == "" && mountPoints[name] {
			continue
		}
		child := rel + "/" + name
		var st unix.Stat_t
		if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue // It went away between the readdir and the stat.
		}
		switch {
		case isWhiteout(&st):
			err = w.add(Change{Path: child, Kind: ChangeDeleted, Seeded: seededPaths[child]})
		case st.Mode&unix.S_IFMT == unix.S_IFDIR:
			err = w.directory(dirfd, name, child, &st)
		default:
			err = w.add(w.entry(child, &st))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *changeWalker) directory(parentfd int, name, rel string, st *unix.Stat_t) error {
	fd, err := unix.Openat(parentfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil // Replaced by something that is no longer a directory.
	}
	defer func() { _ = unix.Close(fd) }()

	change := w.entry(rel, st)
	// New directories can also be opaque. It is a replacement only when
	// there was a path in the base image for the opaque directory to hide.
	if change.Kind == ChangeModified && isOpaque(fd) {
		change.Kind = ChangeReplaced
	}
	// A directory the image already had is in the upper layer only because
	// something below it was copied up, which the entries below will report.
	if change.Kind != ChangeModified {
		if err := w.add(change); err != nil {
			return err
		}
	}
	return w.walk(fd, rel)
}

func (w *changeWalker) entry(rel string, st *unix.Stat_t) Change {
	kind := ChangeAdded
	if w.inLower(rel) {
		kind = ChangeModified
	}
	return Change{
		Path:    rel,
		Kind:    kind,
		Type:    changeType(st.Mode),
		Size:    st.Size,
		Mode:    os.FileMode(st.Mode & 0o7777),
		ModTime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		Seeded:  seededPaths[rel],
	}
}

// inLower reports whether the image already had this path, which is what
// separates a file the workload created from one it edited.
func (w *changeWalker) inLower(rel string) bool {
	if w.lowerFD < 0 {
		return false
	}
	var st unix.Stat_t
	return unix.Fstatat(w.lowerFD, rel[1:], &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

func (w *changeWalker) add(change Change) error {
	if len(w.entries) >= maxChanges {
		w.truncated = true
		return errTruncated
	}
	w.entries = append(w.entries, change)
	return nil
}

// isWhiteout reports an overlayfs deletion marker: a character device with
// device number 0, which no real device node has.
func isWhiteout(st *unix.Stat_t) bool {
	return st.Mode&unix.S_IFMT == unix.S_IFCHR && st.Rdev == 0
}

func isOpaque(fd int) bool {
	buf := make([]byte, 1)
	for _, attr := range []string{opaqueXattr, opaqueUserXattr} {
		if n, err := unix.Fgetxattr(fd, attr, buf); err == nil && n == 1 && buf[0] == 'y' {
			return true
		}
	}
	return false
}

func changeType(mode uint32) string {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return "dir"
	case unix.S_IFREG:
		return "file"
	case unix.S_IFLNK:
		return "symlink"
	default:
		return "special"
	}
}

// readNames lists a directory without taking the caller's descriptor: the
// duplicate carries the read, and closing it leaves the original open.
func readNames(dirfd int) ([]string, error) {
	dup, err := unix.Dup(dirfd)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dup), "") // #nosec G115 -- dup returns a valid descriptor.
	defer func() { _ = dir.Close() }()
	return dir.Readdirnames(-1)
}
