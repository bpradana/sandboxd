//go:build linux

package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bpradana/sandboxd/internal/snapshot"
)

func (e *linuxEngine) CaptureSnapshot(ctx context.Context, id string, dst io.Writer, maxBytes int64) error {
	e.mu.Lock()
	inst, ok := e.instances[id]
	e.mu.Unlock()
	if !ok {
		return ErrSandboxNotFound
	}
	inst.teardownMu.Lock()
	defer inst.teardownMu.Unlock()
	if inst.destroying.Load() && !inst.snapshotting.Load() {
		return ErrSandboxNotFound
	}
	inst.snapshotting.Store(true)
	inst.destroying.Store(true)
	// All sandbox writers, including background jobs and detached terminals,
	// must be dead before reading the filesystem. Never unmount it here.
	if err := e.cgroups.Destroy(ctx, id); err != nil {
		return err
	}
	select {
	case <-inst.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	inst.closePipes()
	if err := lockSnapshotFS(ctx, inst); err != nil {
		return err
	}
	defer inst.fsMu.Unlock()
	return exportFilesystem(ctx, inst.rootfsDir(), dst, e.sandboxCfg.UIDMapBase, e.sandboxCfg.UIDMapSize, maxBytes)
}

// CaptureCheckpoint preserves processes and terminals. Freeze only while
// copying locally; callers upload after thawing. This is crash-consistent
// filesystem state, not a database transaction or a process-memory backup.
func (e *linuxEngine) CaptureCheckpoint(ctx context.Context, id string, dst io.Writer, maxBytes int64) (err error) {
	inst, err := e.running(id)
	if err != nil {
		return err
	}
	inst.teardownMu.Lock()
	defer inst.teardownMu.Unlock()
	if inst.destroying.Load() {
		return ErrSandboxNotFound
	}
	// Drain host file operations before freezing, so an upload blocked on a
	// sandbox process cannot deadlock with the freezer.
	if err := lockSnapshotFS(ctx, inst); err != nil {
		return err
	}
	defer inst.fsMu.Unlock()
	// Thaw even if freezing, capture, or cancellation fails halfway through.
	defer func() {
		if thawErr := e.cgroups.Thaw(id); thawErr != nil {
			inst.snapshotting.Store(true)
			inst.destroying.Store(true)
			err = errors.Join(err, ErrCheckpointThaw, thawErr)
		}
	}()
	if err = e.cgroups.Freeze(ctx, id); err != nil {
		return err
	}
	return exportFilesystem(ctx, inst.rootfsDir(), dst, e.sandboxCfg.UIDMapBase, e.sandboxCfg.UIDMapSize, maxBytes)
}

func lockSnapshotFS(ctx context.Context, inst *instance) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !inst.fsMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// exportFilesystem reads the merged overlay, so deletions and opaque overlay
// directories are represented by their final contents, not whiteout files.
func exportFilesystem(ctx context.Context, dir string, dst io.Writer, uidBase, uidRange int, maxBytes int64) error {
	if maxBytes <= 0 {
		return snapshot.ErrTooLarge
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	gz := gzip.NewWriter(&snapshot.LimitedWriter{Writer: dst, Remaining: maxBytes})
	tw := tar.NewWriter(&snapshot.LimitedWriter{Writer: gz, Remaining: maxBytes})
	links := make(map[[2]uint64]string)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		// These mount points are recreated by init, never restored from backup.
		if name == "proc" || name == "sys" || name == "dev" || name == "run" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = root.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		header.Uname, header.Gname = "", ""
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("missing filesystem ownership")
		}
		header.Uid, header.Gid = unshiftID(int(st.Uid), uidBase, uidRange), unshiftID(int(st.Gid), uidBase, uidRange)
		// Restores follow the image import policy: no setuid/setgid bits.
		header.Mode &= 0o1777
		if info.Mode().IsRegular() && st.Nlink > 1 {
			key := [2]uint64{st.Dev, st.Ino}
			if previous, ok := links[key]; ok {
				header.Typeflag, header.Linkname, header.Size = tar.TypeLink, previous, 0
			} else {
				links[key] = header.Name
			}
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, snapshot.ContextReader{Ctx: ctx, Reader: file})
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	return errors.Join(err, tw.Close(), gz.Close())
}

func unshiftID(id, base, size int) int {
	if id < base || id >= base+size {
		return size - 1
	}
	return id - base
}
