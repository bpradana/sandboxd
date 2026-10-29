//go:build linux

package storage

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// tmpfs keeps each directory on its own size-capped tmpfs. Stored bytes are
// memory, charged to whichever cgroup writes them.
type tmpfs struct{}

func (tmpfs) Name() string { return "tmpfs" }

func (tmpfs) ChargesMemory() bool { return true }

func (tmpfs) Create(dir string, opts Options) error {
	limitBytes := opts.Size
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	data := fmt.Sprintf("size=%d,mode=0700", limitBytes)
	if err := unix.Mount("tmpfs", dir, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, data); err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("mount storage tmpfs: %w", err)
	}
	return nil
}

func (tmpfs) Usage(dir string) (int64, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return int64(fs.Blocks-fs.Bfree) * fs.Bsize, nil // #nosec G115 -- block counts fit in int64.
}

func (tmpfs) Remove(dir string) error { return removeTree(dir) }
