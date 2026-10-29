//go:build linux

// Package fsops performs file operations on a sandbox's root filesystem from
// the host without ever leaving it. Every path is resolved with
// openat2(RESOLVE_IN_ROOT|RESOLVE_NO_MAGICLINKS|RESOLVE_NO_XDEV) relative to a
// directory fd of the sandbox root, so absolute symlinks, "..", and
// /proc magic links planted by sandbox code resolve inside the sandbox -- the
// host-root equivalent of the classic `docker cp` symlink escape is
// structurally impossible. Subsequent operations act on the resolved fds or on
// single path components relative to them, never on re-joined host paths.
package fsops

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	// ErrTooLarge means a write exceeded its byte limit; nothing was replaced.
	ErrTooLarge = errors.New("file too large")
	// ErrNotRegular means the path is not a regular file (device, fifo, socket).
	ErrNotRegular = errors.New("not a regular file")
)

const resolveFlags = unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// openat2Retries bounds retries of openat2's EAGAIN, which it returns when a
// concurrent rename races the resolution.
const openat2Retries = 8

// Info describes one filesystem entry.
type Info struct {
	Path       string
	Name       string
	Mode       os.FileMode
	Size       int64
	ModTime    time.Time
	LinkTarget string
}

// Root is an open sandbox root directory. Entries it creates are owned by
// UID/GID (the sandbox user mapped onto host ids).
type Root struct {
	fd  int
	uid int
	gid int
}

func OpenRoot(dir string, uid, gid int) (*Root, error) {
	fd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open sandbox root: %w", err)
	}
	return &Root{fd: fd, uid: uid, gid: gid}, nil
}

func (r *Root) Close() error {
	return unix.Close(r.fd)
}

// Supported reports whether the kernel implements openat2 (Linux 5.6+).
func Supported() error {
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: resolveFlags}
	fd, err := unix.Openat2(unix.AT_FDCWD, "/", &how)
	if err != nil {
		return fmt.Errorf("openat2 unavailable (Linux 5.6+ required): %w", err)
	}
	return unix.Close(fd)
}

func relative(p string) string {
	rel := strings.TrimLeft(path.Clean("/"+p), "/")
	if rel == "" {
		return "."
	}
	return rel
}

// openat resolves p inside the root.
func (r *Root) openat(p string, flags int) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Resolve: resolveFlags} // #nosec G115 -- open flags are non-negative.
	for range openat2Retries {
		fd, err := unix.Openat2(r.fd, relative(p), &how)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		return fd, err
	}
	return -1, unix.EAGAIN
}

// parent opens the directory containing p and returns it with p's final
// component. The root itself has no parent.
func (r *Root) parent(p string) (int, string, error) {
	clean := path.Clean("/" + p)
	if clean == "/" {
		return -1, "", unix.EINVAL
	}
	dirfd, err := r.openat(path.Dir(clean), unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return -1, "", err
	}
	return dirfd, path.Base(clean), nil
}

// Open returns a regular file for reading. A final symlink is followed, inside
// the root. FIFOs and devices are rejected without blocking on them.
func (r *Root) Open(p string) (*os.File, *Info, error) {
	fd, err := r.openat(p, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if err != nil {
		return nil, nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFDIR:
		_ = unix.Close(fd)
		return nil, nil, unix.EISDIR
	default:
		_ = unix.Close(fd)
		return nil, nil, ErrNotRegular
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, nil, err
	}
	info := infoFromStat(path.Clean("/"+p), &st, "")
	return os.NewFile(uintptr(fd), info.Path), &info, nil // #nosec G115 -- fd is a valid descriptor.
}

// ReadFile reads a whole regular file of at most limit bytes.
func (r *Root) ReadFile(p string, limit int64) ([]byte, error) {
	file, _, err := r.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// WriteFile atomically replaces p with the content of src: it writes a hidden
// temporary file in the same directory, then renames it over p. The new file
// is owned by the root's uid/gid with the given permission bits.
func (r *Root) WriteFile(p string, perm os.FileMode, src io.Reader, limit int64) (*Info, error) {
	dirfd, name, err := r.parent(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirfd) }()

	if err := r.refuseDirectory(dirfd, name); err != nil {
		return nil, err
	}

	tmpName, fd, err := createTemp(dirfd, name)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), tmpName) // #nosec G115 -- fd is a valid descriptor.
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = unix.Unlinkat(dirfd, tmpName, 0)
		}
	}()

	written, err := io.Copy(file, io.LimitReader(src, limit+1))
	if err != nil {
		return nil, err
	}
	if written > limit {
		return nil, ErrTooLarge
	}
	if err := unix.Fchown(fd, r.uid, r.gid); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(fd, permBits(perm)); err != nil {
		return nil, err
	}
	if err := unix.Renameat(dirfd, tmpName, dirfd, name); err != nil {
		return nil, err
	}
	committed = true

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	info := infoFromStat(path.Clean("/"+p), &st, "")
	return &info, nil
}

func (r *Root) refuseDirectory(dirfd int, name string) error {
	var st unix.Stat_t
	err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return unix.EISDIR
	}
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}

func createTemp(dirfd int, name string) (string, int, error) {
	for attempt := range 16 {
		tmpName := fmt.Sprintf(".%s.sbx-%d-%d", truncateName(name), os.Getpid(), time.Now().UnixNano()+int64(attempt))
		fd, err := unix.Openat(dirfd, tmpName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		return tmpName, fd, err
	}
	return "", -1, unix.EEXIST
}

func truncateName(name string) string {
	if len(name) > 200 {
		return name[:200]
	}
	return name
}

// Stat describes p without following a final symlink.
func (r *Root) Stat(p string) (*Info, error) {
	clean := path.Clean("/" + p)
	var st unix.Stat_t
	if clean == "/" {
		if err := unix.Fstat(r.fd, &st); err != nil {
			return nil, err
		}
		info := infoFromStat("/", &st, "")
		return &info, nil
	}
	dirfd, name, err := r.parent(clean)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirfd) }()
	return statAt(dirfd, name, clean)
}

func statAt(dirfd int, name, fullPath string) (*Info, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	target := ""
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		buf := make([]byte, unix.PathMax)
		n, err := unix.Readlinkat(dirfd, name, buf)
		if err == nil {
			target = string(buf[:n])
		}
	}
	info := infoFromStat(fullPath, &st, target)
	return &info, nil
}

// ReadDir lists a directory (following a final symlink, inside the root),
// sorted by name.
func (r *Root) ReadDir(p string) ([]Info, error) {
	fd, err := r.openat(p, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), p) // #nosec G115 -- fd is a valid descriptor.
	defer func() { _ = dir.Close() }()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	base := path.Clean("/" + p)
	result := make([]Info, 0, len(names))
	for _, name := range names {
		info, err := statAt(fd, name, path.Join(base, name))
		if err != nil {
			// The entry vanished between readdir and stat.
			continue
		}
		result = append(result, *info)
	}
	return result, nil
}

// Mkdir creates p (and, with parents, every missing ancestor) owned by the
// root's uid/gid. With parents an existing directory is not an error.
func (r *Root) Mkdir(p string, perm os.FileMode, parents bool) (*Info, error) {
	clean := path.Clean("/" + p)
	if clean == "/" {
		if parents {
			return r.Stat("/")
		}
		return nil, unix.EEXIST
	}

	targets := []string{clean}
	if parents {
		targets = targets[:0]
		parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
		for i := range parts {
			targets = append(targets, "/"+strings.Join(parts[:i+1], "/"))
		}
	}

	for _, target := range targets {
		if err := r.mkdirOne(target, perm, parents); err != nil {
			return nil, err
		}
	}
	return r.Stat(clean)
}

func (r *Root) mkdirOne(target string, perm os.FileMode, allowExisting bool) error {
	dirfd, name, err := r.parent(target)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirfd) }()

	err = unix.Mkdirat(dirfd, name, permBits(perm))
	if errors.Is(err, unix.EEXIST) && allowExisting {
		// Must already be a directory (possibly via a symlink inside the root).
		fd, openErr := r.openat(target, unix.O_PATH|unix.O_DIRECTORY)
		if openErr != nil {
			return openErr
		}
		return unix.Close(fd)
	}
	if err != nil {
		return err
	}
	if err := unix.Fchownat(dirfd, name, r.uid, r.gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	// Apply the exact mode regardless of the daemon's umask.
	return unix.Fchmodat(dirfd, name, permBits(perm), 0)
}

// Remove deletes p. A symlink is removed, never followed. Directories must be
// empty unless recursive is set.
func (r *Root) Remove(p string, recursive bool) error {
	dirfd, name, err := r.parent(p)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirfd) }()

	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(dirfd, name, 0)
	}
	if recursive {
		return removeAllAt(dirfd, name)
	}
	return unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
}

// removeAllAt removes name under dirfd recursively. Children are opened with
// O_NOFOLLOW relative to their parent fd and names come from readdir, so the
// walk can neither follow symlinks nor climb out through "..".
func removeAllAt(dirfd int, name string) error {
	err := unix.Unlinkat(dirfd, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	if !errors.Is(err, unix.EISDIR) && !errors.Is(err, unix.EPERM) {
		return err
	}

	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name) // #nosec G115 -- fd is a valid descriptor.
	for {
		names, readErr := dir.Readdirnames(256)
		for _, child := range names {
			if err := removeAllAt(fd, child); err != nil {
				_ = dir.Close()
				return err
			}
		}
		if readErr != nil {
			break
		}
	}
	_ = dir.Close()

	err = unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func permBits(perm os.FileMode) uint32 {
	bits := uint32(perm.Perm())
	if perm&os.ModeSticky != 0 {
		bits |= unix.S_ISVTX
	}
	return bits
}

func infoFromStat(fullPath string, st *unix.Stat_t, linkTarget string) Info {
	mode := os.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= os.ModeDir
	case unix.S_IFLNK:
		mode |= os.ModeSymlink
	case unix.S_IFIFO:
		mode |= os.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= os.ModeSocket
	case unix.S_IFCHR:
		mode |= os.ModeDevice | os.ModeCharDevice
	case unix.S_IFBLK:
		mode |= os.ModeDevice
	}
	if st.Mode&unix.S_ISUID != 0 {
		mode |= os.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= os.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= os.ModeSticky
	}
	name := path.Base(fullPath)
	return Info{
		Path:       fullPath,
		Name:       name,
		Mode:       mode,
		Size:       st.Size,
		ModTime:    time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
		LinkTarget: linkTarget,
	}
}
