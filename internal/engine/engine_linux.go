//go:build linux

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine/cgroup"
	"github.com/bpradana/sandboxd/internal/engine/image"
	"github.com/bpradana/sandboxd/internal/engine/netns"
	"github.com/bpradana/sandboxd/internal/engine/storage"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

const (
	sandboxesDirName = "sandboxes"
	// startTimeout bounds init's in-sandbox setup.
	startTimeout = 30 * time.Second
	// initOOMScoreAdj makes init unkillable by the OOM killer, so a runaway
	// command (or a full tmpfs) never ends the whole sandbox; the shim resets
	// commands to 0.
	initOOMScoreAdj = "-1000"
	exitBuffer      = 256
	sandboxDirMode  = 0o711
)

type linuxEngine struct {
	logger     *logging.Logger
	sandboxCfg config.SandboxConfig
	execCfg    config.ExecConfig
	netCfg     config.NetworkConfig

	sandboxesDir string
	images       *image.Store
	cgroups      *cgroup.Manager
	storage      storage.Driver
	network      *netns.Manager
	self         *os.File

	mu        sync.Mutex
	instances map[string]*instance
	readyErr  error
	stopped   bool
	exits     chan Exit
}

func New(logger *logging.Logger, sandboxCfg config.SandboxConfig, execCfg config.ExecConfig, netCfg config.NetworkConfig) Engine {
	return &linuxEngine{
		logger:       logger,
		sandboxCfg:   sandboxCfg,
		execCfg:      execCfg,
		netCfg:       netCfg,
		sandboxesDir: filepath.Join(sandboxCfg.StateDir, sandboxesDirName),
		images:       image.NewStore(sandboxCfg.ImagesDir, sandboxCfg.UIDMapBase, sandboxCfg.UIDMapSize),
		instances:    make(map[string]*instance),
		readyErr:     ErrNotReady,
		exits:        make(chan Exit, exitBuffer),
	}
}

func (e *linuxEngine) Start(ctx context.Context) error {
	if err := preflight(); err != nil {
		return fmt.Errorf("sandbox engine preflight: %w", err)
	}
	for _, dir := range []string{e.sandboxCfg.StateDir, e.sandboxCfg.ImagesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- state and image dirs must be traversable by sandbox root.
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// Traverse-only: sandbox init (host uid UIDMapBase) must reach its rootfs
	// by path, but nobody besides root may list sandboxes.
	if err := os.MkdirAll(e.sandboxesDir, sandboxDirMode); err != nil {
		return fmt.Errorf("create %s: %w", e.sandboxesDir, err)
	}
	if err := os.Chmod(e.sandboxesDir, sandboxDirMode); err != nil {
		return err
	}
	if err := makePrivateMount(e.sandboxesDir); err != nil {
		return err
	}

	cgroups, err := cgroup.Setup(e.sandboxCfg.CgroupRoot)
	if err != nil {
		return fmt.Errorf("cgroup setup: %w", err)
	}
	e.cgroups = cgroups

	driver, err := storage.Open(e.sandboxCfg.StorageDriver, e.sandboxCfg.StorageFS)
	if err != nil {
		return fmt.Errorf("storage setup: %w", err)
	}
	e.storage = driver

	e.reconcile(ctx)
	e.images.CleanupTemp()
	if err := storage.Probe(driver, e.sandboxesDir); err != nil {
		return fmt.Errorf("storage setup: %w", err)
	}

	if e.netCfg.Enabled {
		network, err := netns.Setup(netns.Config{
			Bridge:     e.netCfg.BridgeName,
			CIDR:       e.netCfg.CIDR,
			DNS:        e.netCfg.DNS,
			EgressDeny: e.netCfg.EgressDeny,
			IPForward:  e.netCfg.IPForward,
		})
		if err != nil {
			return fmt.Errorf("network setup: %w", err)
		}
		e.network = network
	}

	self, err := sealedSelf()
	if err != nil {
		return fmt.Errorf("seal executable: %w", err)
	}
	e.self = self

	e.mu.Lock()
	e.readyErr = nil
	e.mu.Unlock()
	e.logger.Info("sandbox engine started",
		zap.String("cgroup_root", cgroups.Root()),
		zap.String("state_dir", e.sandboxCfg.StateDir),
		zap.String("storage", e.storage.Name()),
		zap.Bool("egress_enabled", e.network != nil),
	)
	return nil
}

// reconcile removes everything a previous process left behind. State is not
// persisted, so no leftover sandbox can be resumed.
func (e *linuxEngine) reconcile(ctx context.Context) {
	if removed, err := e.cgroups.Reconcile(ctx); err != nil {
		e.logger.Warn("reconcile cgroups", zap.Error(err))
	} else if len(removed) > 0 {
		e.logger.Info("removed leftover sandbox cgroups", zap.Int("count", len(removed)))
	}

	entries, err := os.ReadDir(e.sandboxesDir)
	if err != nil {
		e.logger.Warn("reconcile sandbox dirs", zap.Error(err))
	}
	for _, entry := range entries {
		dir := filepath.Join(e.sandboxesDir, entry.Name())
		if err := removeSandboxDir(e.storage, dir); err != nil {
			e.logger.Warn("remove leftover sandbox dir", zap.String("dir", dir), zap.Error(err))
		}
	}

	if err := netns.Cleanup(e.netCfg.BridgeName); err != nil {
		e.logger.Warn("reconcile network", zap.Error(err))
	}
}

func (e *linuxEngine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	e.readyErr = ErrNotReady
	instances := make([]*instance, 0, len(e.instances))
	for _, inst := range e.instances {
		inst.destroying.Store(true)
		instances = append(instances, inst)
	}
	e.mu.Unlock()

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(16)
	for _, inst := range instances {
		group.Go(func() error {
			if err := e.teardown(groupCtx, inst); err != nil {
				e.logger.Error("destroy sandbox on shutdown", zap.String("sandbox_id", inst.id), zap.Error(err))
			}
			return nil
		})
	}
	_ = group.Wait()

	var errs []error
	if e.network != nil {
		if err := e.network.Teardown(); err != nil {
			errs = append(errs, fmt.Errorf("network teardown: %w", err))
		}
	}
	if err := unix.Unmount(e.sandboxesDir, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) {
		errs = append(errs, fmt.Errorf("unmount %s: %w", e.sandboxesDir, err))
	}
	if e.self != nil {
		_ = e.self.Close()
	}

	e.mu.Lock()
	close(e.exits)
	e.mu.Unlock()
	e.logger.Info("sandbox engine stopped", zap.Int("destroyed", len(instances)))
	return errors.Join(errs...)
}

func (e *linuxEngine) Ready() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.readyErr
}

func (e *linuxEngine) Exits() <-chan Exit {
	return e.exits
}

func (e *linuxEngine) Images(context.Context) ([]Image, error) {
	return listImages(e.images)
}

func (e *linuxEngine) Create(ctx context.Context, spec Spec) (*Info, error) {
	if err := e.Ready(); err != nil {
		return nil, err
	}
	lower, err := e.images.Path(spec.Image)
	if err != nil {
		if errors.Is(err, image.ErrNotFound) || errors.Is(err, image.ErrInvalidName) {
			return nil, ErrImageNotFound
		}
		return nil, err
	}

	inst := newInstance(spec, filepath.Join(e.sandboxesDir, spec.ID), e.execCfg.MaxConcurrent)
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil, ErrNotReady
	}
	if _, exists := e.instances[spec.ID]; exists {
		e.mu.Unlock()
		return nil, fmt.Errorf("sandbox %s already exists", spec.ID)
	}
	e.instances[spec.ID] = inst
	e.mu.Unlock()

	info, err := e.start(ctx, inst, lower)
	if err != nil {
		inst.destroying.Store(true)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.sandboxCfg.DestroyTimeout)
		defer cancel()
		if teardownErr := e.teardown(cleanupCtx, inst); teardownErr != nil {
			e.logger.Error("roll back failed sandbox", zap.String("sandbox_id", spec.ID), zap.Error(teardownErr))
		}
		return nil, err
	}
	inst.started.Store(true)
	return info, nil
}

// start builds the sandbox in order: cgroup → storage → rootfs content →
// init process (born inside the cgroup and new namespaces) → network →
// config handoff → ready handshake. teardown undoes whatever was done.
func (e *linuxEngine) start(ctx context.Context, inst *instance, lower string) (*Info, error) {
	if err := os.Mkdir(inst.dir, sandboxDirMode); err != nil {
		return nil, fmt.Errorf("create sandbox dir: %w", err)
	}

	_, cgroupFile, err := e.cgroups.Create(inst.id, cgroup.Limits{
		CPUMillis: inst.spec.Limits.CPUMillis,
		MemoryMB:  inst.spec.Limits.MemoryMB,
		StorageMB: e.memoryStorageMB(inst),
		PidsMax:   inst.spec.Limits.PidsMax,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = cgroupFile.Close() }()

	if err := e.mountStorage(inst, lower); err != nil {
		return nil, err
	}
	if err := prepareRootfs(inst, e.sandboxCfg, e.dnsServers()); err != nil {
		return nil, err
	}

	if err := e.launchInit(ctx, inst, cgroupFile); err != nil {
		return nil, err
	}

	if inst.spec.Network == consts.NetworkEgress {
		if e.network == nil {
			return nil, errors.New("egress networking is disabled")
		}
		attachment, err := e.network.Attach(inst.id, inst.pid)
		if err != nil {
			return nil, fmt.Errorf("attach network: %w", err)
		}
		inst.attachment = attachment
	} else if err := netns.LoopbackOnly(inst.pid); err != nil {
		return nil, fmt.Errorf("configure loopback: %w", err)
	}

	if err := e.handshake(ctx, inst); err != nil {
		return nil, err
	}

	info := &Info{InitPID: inst.pid}
	if inst.attachment != nil {
		info.IPAddress = inst.attachment.IP.String()
	}
	return info, nil
}

func (e *linuxEngine) dnsServers() []string {
	if e.network != nil {
		return e.network.DNS()
	}
	return nil
}

// handshake sends init its config (which also signals that host-side setup
// is complete) and waits for the ready status.
func (e *linuxEngine) handshake(ctx context.Context, inst *instance) error {
	payload, err := json.Marshal(inst.initConfig(e.sandboxCfg, e.execCfg, e.memoryStorageMB(inst)))
	if err != nil {
		return err
	}
	_, writeErr := inst.configPipe.Write(payload)
	_ = inst.configPipe.Close()
	inst.configPipe = nil
	if writeErr != nil {
		return fmt.Errorf("send init config: %w", writeErr)
	}

	deadline := time.Now().Add(startTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = inst.statusPipe.SetReadDeadline(deadline)
	status, err := io.ReadAll(io.LimitReader(inst.statusPipe, 64<<10))
	_ = inst.statusPipe.Close()
	inst.statusPipe = nil
	if err != nil {
		return fmt.Errorf("wait for sandbox init: %w", err)
	}
	message := strings.TrimSpace(string(status))
	if message != "ok" {
		if message == "" {
			message = "init exited during setup"
		}
		return fmt.Errorf("sandbox init failed: %s", message)
	}
	return nil
}

// watch reports init's exit unless the engine is tearing the sandbox down.
func (e *linuxEngine) watch(inst *instance) {
	err := inst.cmd.Wait()
	close(inst.done)
	if inst.destroying.Load() || !inst.started.Load() {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return
	}
	select {
	case e.exits <- Exit{SandboxID: inst.id, Err: err}:
	default:
		e.logger.Error("exit event dropped", zap.String("sandbox_id", inst.id))
	}
}

func (e *linuxEngine) Destroy(ctx context.Context, id string) error {
	e.mu.Lock()
	inst, ok := e.instances[id]
	e.mu.Unlock()
	if !ok {
		return ErrSandboxNotFound
	}
	inst.destroying.Store(true)
	return e.teardown(ctx, inst)
}

// teardown releases every resource of inst in reverse creation order. Each
// step tolerates the resource being absent, so it is safe on partial starts.
func (e *linuxEngine) teardown(ctx context.Context, inst *instance) error {
	inst.teardownMu.Lock()
	defer inst.teardownMu.Unlock()

	var errs []error
	if err := e.cgroups.Destroy(ctx, inst.id); err != nil {
		errs = append(errs, err)
	}
	inst.closePipes()
	if inst.cmd != nil {
		select {
		case <-inst.done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf("init did not exit: %w", ctx.Err()))
		}
	}
	if e.network != nil && inst.attachment != nil {
		if err := e.network.Detach(inst.attachment); err != nil {
			errs = append(errs, fmt.Errorf("detach network: %w", err))
		}
		inst.attachment = nil
	}
	if err := removeSandboxDir(e.storage, inst.dir); err != nil {
		errs = append(errs, err)
	}

	if len(errs) == 0 {
		e.mu.Lock()
		delete(e.instances, inst.id)
		e.mu.Unlock()
	}
	return errors.Join(errs...)
}

// running returns a live instance or ErrSandboxNotFound.
func (e *linuxEngine) running(id string) (*instance, error) {
	e.mu.Lock()
	inst, ok := e.instances[id]
	e.mu.Unlock()
	if !ok || inst.destroying.Load() {
		return nil, ErrSandboxNotFound
	}
	select {
	case <-inst.done:
		return nil, ErrSandboxNotFound
	default:
	}
	return inst, nil
}

func (e *linuxEngine) Stats(_ context.Context, id string) (*Stats, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	cg, err := e.cgroups.Stats(id)
	if err != nil {
		return nil, err
	}
	stats := &Stats{
		MemoryBytes:      cg.MemoryBytes,
		MemoryPeakBytes:  cg.MemoryPeakBytes,
		MemoryLimitBytes: cg.MemoryLimitBytes,
		CPUUsageMicros:   cg.CPUUsageMicros,
		PidsCurrent:      cg.PidsCurrent,
		PidsLimit:        cg.PidsLimit,
		OOMKills:         cg.OOMKills,
	}
	stats.StorageLimitBytes = int64(inst.spec.Limits.StorageMB) << 20
	if used, err := e.storage.Usage(inst.scratchDir()); err == nil {
		stats.StorageUsedBytes = used
	}
	return stats, nil
}

// memoryStorageMB is the storage that counts as memory: all of it when the
// writable layer is a tmpfs, none when it is on disk.
func (e *linuxEngine) memoryStorageMB(inst *instance) int {
	if e.storage.ChargesMemory() {
		return inst.spec.Limits.StorageMB
	}
	return 0
}
