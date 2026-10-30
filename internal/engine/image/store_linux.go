//go:build linux

package image

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	tmpPrefix = ".import-"
	// resolveFlags confine every extraction path to the image being built.
	resolveFlags = unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV
)

type Store struct {
	dir     string
	uidBase int
	idRange int
}

func NewStore(dir string, uidBase, idRange int) *Store {
	return &Store{dir: dir, uidBase: uidBase, idRange: idRange}
}

func (s *Store) Path(name string) (string, error) {
	if !ValidName(name) {
		return "", ErrInvalidName
	}
	dir := filepath.Join(s.dir, name)
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrNotFound
	}
	return dir, nil
}

func (s *Store) List() ([]Image, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Image{}, nil
	}
	if err != nil {
		return nil, err
	}
	images := make([]Image, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !ValidName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		images = append(images, Image{Name: entry.Name(), CreatedAt: info.ModTime()})
	}
	sort.Slice(images, func(i, j int) bool { return images[i].Name < images[j].Name })
	return images, nil
}

func (s *Store) Remove(name string) error {
	dir, err := s.Path(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Import extracts a (optionally gzip-compressed) root filesystem tarball as
// image name. Extraction happens in a temporary directory that is renamed into
// place only when complete. Entries are confined to the image root, device
// nodes are skipped, setuid/setgid bits are stripped, and ownership is shifted
// into the sandbox id range.
func (s *Store) Import(ctx context.Context, name string, src io.Reader) error {
	return s.importArchive(ctx, name, src, 0)
}

// ImportSnapshot bounds decompressed data, including tar headers, so a small
// compressed object cannot exhaust the host disk during restore.
func (s *Store) ImportSnapshot(ctx context.Context, name string, src io.Reader, maxBytes int64) error {
	if maxBytes <= 0 {
		return fmt.Errorf("snapshot expansion limit must be positive")
	}
	return s.importArchive(ctx, name, src, maxBytes)
}

func (s *Store) importArchive(ctx context.Context, name string, src io.Reader, maxBytes int64) error {
	if !ValidName(name) {
		return ErrInvalidName
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil { // #nosec G301 -- overlay lower dirs must be readable by sandbox root.
		return err
	}
	final := filepath.Join(s.dir, name)
	if _, err := os.Lstat(final); err == nil {
		return ErrExists
	}

	tmp, err := os.MkdirTemp(s.dir, tmpPrefix+name+"-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmp)
		}
	}()

	if err := s.extract(ctx, tmp, src, maxBytes); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { // #nosec G302 -- image root becomes the sandbox's "/".
		return err
	}
	if err := os.Lchown(tmp, s.uidBase, s.uidBase); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Store) extract(ctx context.Context, root string, src io.Reader, maxBytes int64) error {
	reader, err := decompress(src)
	if err != nil {
		return err
	}
	if closer, ok := reader.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	if maxBytes > 0 {
		reader = &expansionReader{reader: reader, remaining: maxBytes}
	}
	rootfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rootfd) }()

	type dirTime struct {
		name  string
		mtime time.Time
	}
	var dirs []dirTime

	archive := tar.NewReader(reader)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name := path.Clean("/" + header.Name)
		if name == "/" {
			continue
		}
		if err := s.extractEntry(rootfd, name, header, archive); err != nil {
			return fmt.Errorf("extract %s: %w", header.Name, err)
		}
		if header.Typeflag == tar.TypeDir {
			dirs = append(dirs, dirTime{name: name, mtime: header.ModTime})
		}
	}

	// Directory mtimes change as children are created; restore them last.
	for i := len(dirs) - 1; i >= 0; i-- {
		times := []unix.Timespec{unix.NsecToTimespec(dirs[i].mtime.UnixNano()), unix.NsecToTimespec(dirs[i].mtime.UnixNano())}
		dirfd, base, err := parentAt(rootfd, dirs[i].name)
		if err != nil {
			continue
		}
		_ = unix.UtimesNanoAt(dirfd, base, times, unix.AT_SYMLINK_NOFOLLOW)
		_ = unix.Close(dirfd)
	}
	return nil
}

type expansionReader struct {
	reader    io.Reader
	remaining int64
}

func (r *expansionReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, fmt.Errorf("snapshot expansion limit exceeded")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (s *Store) extractEntry(rootfd int, name string, header *tar.Header, content io.Reader) error {
	dirfd, base, err := s.ensureParent(rootfd, name)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(dirfd) }()

	uid, gid := s.shift(header.Uid), s.shift(header.Gid)
	mode := uint32(header.Mode) & 0o1777 // #nosec G115 -- masked to permission bits; setuid/setgid stripped.

	switch header.Typeflag {
	case tar.TypeDir:
		if err := unix.Mkdirat(dirfd, base, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
	case tar.TypeReg:
		_ = unix.Unlinkat(dirfd, base, 0)
		fd, err := unix.Openat(dirfd, base, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(fd), base) // #nosec G115 -- fd is a valid descriptor.
		_, copyErr := io.Copy(file, content)  // #nosec G110 -- operator-supplied image; size bounded by the tarball.
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	case tar.TypeSymlink:
		_ = unix.Unlinkat(dirfd, base, 0)
		// The target is stored verbatim; it is only ever resolved inside a
		// sandbox's mount namespace or through openat2(RESOLVE_IN_ROOT).
		if err := unix.Symlinkat(header.Linkname, dirfd, base); err != nil {
			return err
		}
	case tar.TypeLink:
		targetDir, targetBase, err := parentAt(rootfd, path.Clean("/"+header.Linkname))
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(targetDir) }()
		_ = unix.Unlinkat(dirfd, base, 0)
		return unix.Linkat(targetDir, targetBase, dirfd, base, 0)
	case tar.TypeFifo:
		if err := unix.Mkfifoat(dirfd, base, 0o600); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
	default:
		// Device nodes, sockets, and vendor extensions are skipped: sandbox
		// /dev is built at start from a fixed allow-list.
		return nil
	}

	if err := unix.Fchownat(dirfd, base, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if header.Typeflag != tar.TypeSymlink {
		if err := unix.Fchmodat(dirfd, base, mode, 0); err != nil {
			return err
		}
	}
	times := []unix.Timespec{unix.NsecToTimespec(header.ModTime.UnixNano()), unix.NsecToTimespec(header.ModTime.UnixNano())}
	_ = unix.UtimesNanoAt(dirfd, base, times, unix.AT_SYMLINK_NOFOLLOW)
	return nil
}

// ensureParent creates missing parent directories of name (tarballs do not
// always list them) and returns an fd of the parent plus the final component.
func (s *Store) ensureParent(rootfd int, name string) (int, string, error) {
	dir := path.Dir(name)
	if dir != "/" {
		parts := strings.Split(strings.TrimPrefix(dir, "/"), "/")
		for i := range parts {
			current := "/" + strings.Join(parts[:i+1], "/")
			parentfd, base, err := parentAt(rootfd, current)
			if err != nil {
				return -1, "", err
			}
			err = unix.Mkdirat(parentfd, base, 0o755)
			if err == nil {
				_ = unix.Fchownat(parentfd, base, s.uidBase, s.uidBase, unix.AT_SYMLINK_NOFOLLOW)
			}
			_ = unix.Close(parentfd)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				return -1, "", err
			}
		}
	}
	return parentAt(rootfd, name)
}

func parentAt(rootfd int, name string) (int, string, error) {
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: resolveFlags}
	rel := strings.TrimPrefix(path.Dir(name), "/")
	if rel == "" {
		rel = "."
	}
	fd, err := unix.Openat2(rootfd, rel, &how)
	if err != nil {
		return -1, "", err
	}
	return fd, path.Base(name), nil
}

// shift maps an image uid/gid into the sandbox range; ids outside it collapse
// to the range's "nobody" (the last id).
func (s *Store) shift(id int) int {
	if id < 0 || id >= s.idRange {
		id = s.idRange - 1
	}
	return s.uidBase + id
}

func decompress(src io.Reader) (io.Reader, error) {
	buffered := bufio.NewReader(src)
	magic, err := buffered.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		return gzip.NewReader(buffered)
	}
	return buffered, nil
}

// CleanupTemp removes half-finished imports.
func (s *Store) CleanupTemp() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tmpPrefix) {
			_ = os.RemoveAll(filepath.Join(s.dir, entry.Name()))
		}
	}
}
