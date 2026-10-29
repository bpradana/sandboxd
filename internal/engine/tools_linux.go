//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/engine/cgroup"
	"github.com/bpradana/sandboxd/internal/engine/image"
	"github.com/bpradana/sandboxd/internal/engine/initproc"
	"github.com/bpradana/sandboxd/internal/engine/netns"
	"github.com/bpradana/sandboxd/internal/engine/reexec"
	"github.com/bpradana/sandboxd/internal/engine/shim"
	"github.com/bpradana/sandboxd/internal/engine/storage"
)

// Reexec runs the sandbox init or shim entrypoint when this process was
// re-executed as one of them, and reports whether it did (entrypoints exit
// the process themselves). It must be the first call in main.
func Reexec() bool {
	reexec.Register(reexec.InitName, initproc.Main)
	reexec.Register(reexec.ShimName, shim.Main)
	return reexec.Dispatch()
}

func ImportImage(ctx context.Context, cfg config.SandboxConfig, name string, src io.Reader) error {
	return image.NewStore(cfg.ImagesDir, cfg.UIDMapBase, cfg.UIDMapSize).Import(ctx, name, src)
}

func RemoveImage(cfg config.SandboxConfig, name string) error {
	return image.NewStore(cfg.ImagesDir, cfg.UIDMapBase, cfg.UIDMapSize).Remove(name)
}

func ListImages(cfg config.SandboxConfig) ([]Image, error) {
	return listImages(image.NewStore(cfg.ImagesDir, cfg.UIDMapBase, cfg.UIDMapSize))
}

func listImages(store *image.Store) ([]Image, error) {
	images, err := store.List()
	if err != nil {
		return nil, err
	}
	result := make([]Image, 0, len(images))
	for _, img := range images {
		result = append(result, Image{Name: img.Name, CreatedAt: img.CreatedAt})
	}
	return result, nil
}

// Cleanup removes everything a crashed server left behind: sandbox cgroups,
// mounts and directories, veths, the bridge, and the nftables table. With an
// empty SANDBOX_CGROUP_ROOT it assumes it runs in the server's cgroup tree.
func Cleanup(ctx context.Context, sandboxCfg config.SandboxConfig, netCfg config.NetworkConfig) error {
	var errs []error

	root := sandboxCfg.CgroupRoot
	if root == "" {
		self, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			return err
		}
		root = cgroup.DefaultRoot(strings.TrimPrefix(strings.TrimSpace(string(self)), "0::"))
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		errs = append(errs, err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), cgroup.SandboxPrefix) {
			if err := cgroup.DestroyPath(ctx, filepath.Join(root, entry.Name())); err != nil {
				errs = append(errs, err)
			}
		}
	}

	sandboxesDir := filepath.Join(sandboxCfg.StateDir, sandboxesDirName)
	dirs, err := os.ReadDir(sandboxesDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, err)
	}
	// Without a usable driver, storage is removed as a plain tree.
	driver, _ := storage.Open(sandboxCfg.StorageDriver, sandboxCfg.StorageFS)
	for _, dir := range dirs {
		if err := removeSandboxDir(driver, filepath.Join(sandboxesDir, dir.Name())); err != nil {
			errs = append(errs, err)
		}
	}

	if err := netns.Cleanup(netCfg.BridgeName); err != nil {
		errs = append(errs, fmt.Errorf("network cleanup: %w", err))
	}
	return errors.Join(errs...)
}
