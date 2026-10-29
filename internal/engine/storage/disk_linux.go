//go:build linux

package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// diskImage gives each directory its own filesystem, made in a sparse image
// file next to it (dir + ".img") and mounted through a loop device. The image
// size is the hard limit, so no filesystem quota support is needed and any
// state filesystem works. Space is allocated as data is written and returned
// on delete (the mount discards freed blocks, which punches holes in the
// image).
type diskImage struct {
	fs string
}

// Kernel ABI (include/uapi/linux/loop.h).
const (
	loopCtlGetFree       = 0x4c82
	loopConfigure        = 0x4c0a // Linux 5.8
	loopClearFd          = 0x4c01
	loopMajor            = 7
	loFlagsAutoclear     = 4
	loFlagsDirectIO      = 16
	loopAttachAttempts   = 16
	mkfsTimeout          = time.Minute
	btrfsMixedBelowBytes = 256 << 20
)

type loopInfo64 struct {
	Device         uint64
	Inode          uint64
	Rdevice        uint64
	Offset         uint64
	SizeLimit      uint64
	Number         uint32
	EncryptType    uint32
	EncryptKeySize uint32
	Flags          uint32
	FileName       [64]byte
	CryptName      [64]byte
	EncryptKey     [32]byte
	Init           [2]uint64
}

type loopConfig struct {
	Fd        uint32
	BlockSize uint32
	Info      loopInfo64
	Reserved  [8]uint64
}

// supportedFilesystems are tried in order when no filesystem is configured.
var supportedFilesystems = []string{"ext4", "btrfs"}

func newDiskImage(fs string) (*diskImage, error) {
	candidates := supportedFilesystems
	if fs != "" {
		candidates = []string{fs}
	}
	available, err := os.ReadFile("/proc/filesystems")
	if err != nil {
		return nil, err
	}
	var reasons []string
	for _, candidate := range candidates {
		if !strings.Contains(string(available), "\t"+candidate+"\n") {
			reasons = append(reasons, candidate+": not supported by the kernel")
			continue
		}
		if _, err := exec.LookPath("mkfs." + candidate); err != nil {
			reasons = append(reasons, candidate+": mkfs."+candidate+" not found")
			continue
		}
		if _, err := os.Stat("/dev/loop-control"); err != nil {
			return nil, fmt.Errorf("disk storage needs loop devices: %w", err)
		}
		return &diskImage{fs: candidate}, nil
	}
	return nil, fmt.Errorf("no filesystem for disk storage (%s); install e2fsprogs or btrfs-progs, or set SANDBOX_STORAGE_DRIVER=tmpfs",
		strings.Join(reasons, "; "))
}

func (d *diskImage) Name() string { return "disk/" + d.fs }

func (d *diskImage) ChargesMemory() bool { return false }

func (d *diskImage) Create(dir string, limitBytes int64) error {
	if limitBytes < MinBytes {
		return fmt.Errorf("storage of %d bytes is below the minimum of %d", limitBytes, MinBytes)
	}
	image := dir + ".img"
	file, err := os.OpenFile(image, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- engine-built path.
	if err != nil {
		return err
	}
	err = file.Truncate(limitBytes)
	_ = file.Close()
	if err == nil {
		err = d.mkfs(image, limitBytes)
	}
	if err == nil {
		err = os.Mkdir(dir, 0o700)
	}
	if err == nil {
		err = d.mount(image, dir)
	}
	if err != nil {
		_ = d.Remove(dir)
		return err
	}
	// The filesystem's root replaces the directory; keep it private.
	return os.Chmod(dir, 0o700) // #nosec G302 -- a directory: owner needs search permission.
}

func (d *diskImage) mkfs(image string, size int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), mkfsTimeout)
	defer cancel()
	var args []string
	switch d.fs {
	case "ext4":
		// No journal: a sandbox's storage never outlives a crash of its host
		// process, so recovery is pointless. No reserved blocks.
		args = []string{"-q", "-F", "-m", "0", "-O", "^has_journal", "-E", "nodiscard,lazy_itable_init=1", image}
	case "btrfs":
		args = []string{"-q", "-f", "-K"}
		if size < btrfsMixedBelowBytes {
			args = append(args, "--mixed")
		}
		args = append(args, image)
	default:
		return fmt.Errorf("unsupported filesystem %q", d.fs)
	}
	out, err := exec.CommandContext(ctx, "mkfs."+d.fs, args...).CombinedOutput() // #nosec G204 -- fixed tool, engine-built path.
	if err != nil {
		return fmt.Errorf("mkfs.%s: %w: %s", d.fs, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d *diskImage) mount(image, dir string) error {
	device, loop, err := attachLoop(image)
	if err != nil {
		return err
	}
	// The loop device clears itself once nothing holds it, so the mount
	// must exist before this descriptor closes.
	defer func() { _ = loop.Close() }()
	options := "discard"
	if d.fs == "btrfs" {
		options = "discard=async"
	}
	if err := unix.Mount(device, dir, d.fs, unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
		return fmt.Errorf("mount %s storage: %w", d.fs, err)
	}
	return nil
}

func (d *diskImage) Usage(dir string) (int64, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return int64(fs.Blocks-fs.Bfree) * fs.Bsize, nil // #nosec G115 -- block counts fit in int64.
}

// Remove unmounts and deletes the directory and its image. A loop device
// left attached by a crash is detached too.
func (d *diskImage) Remove(dir string) error {
	if err := removeTree(dir); err != nil {
		return err
	}
	image := dir + ".img"
	if err := os.Remove(image); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	detachLoopsBackedBy(image)
	return nil
}

// attachLoop binds image to a free loop device with direct I/O (no second
// copy in the page cache) when the backing filesystem allows it.
func attachLoop(image string) (string, *os.File, error) {
	backing, err := os.OpenFile(image, os.O_RDWR, 0) // #nosec G304 -- engine-built path.
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = backing.Close() }()
	control, err := os.OpenFile("/dev/loop-control", os.O_RDWR, 0)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = control.Close() }()

	for range loopAttachAttempts {
		number, _, errno := unix.Syscall(unix.SYS_IOCTL, control.Fd(), loopCtlGetFree, 0)
		if errno != 0 {
			return "", nil, fmt.Errorf("find a free loop device: %w", errno)
		}
		device := "/dev/loop" + strconv.Itoa(int(number))   // #nosec G115 -- small device number.
		loop, err := openLoopDevice(device, uint32(number)) // #nosec G115 -- small device number.
		if err != nil {
			return "", nil, err
		}
		config := loopConfig{Fd: uint32(backing.Fd())} // #nosec G115 -- valid descriptor.
		config.Info.Flags = loFlagsAutoclear | loFlagsDirectIO
		err = ioctl(loop.Fd(), loopConfigure, unsafe.Pointer(&config)) // #nosec G103 -- kernel ABI call.
		if errors.Is(err, unix.EINVAL) {
			config.Info.Flags = loFlagsAutoclear
			err = ioctl(loop.Fd(), loopConfigure, unsafe.Pointer(&config)) // #nosec G103 -- kernel ABI call.
		}
		if err == nil {
			return device, loop, nil
		}
		_ = loop.Close()
		if !errors.Is(err, unix.EBUSY) {
			return "", nil, fmt.Errorf("configure %s: %w", device, err)
		}
		// Another process took the device between GET_FREE and CONFIGURE.
	}
	return "", nil, errors.New("no loop device could be attached")
}

// openLoopDevice opens /dev/loopN, creating the node when /dev does not have
// it yet (container /dev is populated only at start).
func openLoopDevice(device string, number uint32) (*os.File, error) {
	loop, err := os.OpenFile(device, os.O_RDWR, 0) // #nosec G304 -- loop device path.
	if errors.Is(err, os.ErrNotExist) {
		if err := unix.Mknod(device, unix.S_IFBLK|0o660, int(unix.Mkdev(loopMajor, number))); err != nil && !errors.Is(err, unix.EEXIST) { // #nosec G115 -- device numbers fit in int.
			return nil, fmt.Errorf("create %s: %w", device, err)
		}
		loop, err = os.OpenFile(device, os.O_RDWR, 0) // #nosec G304 -- loop device path.
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", device, err)
	}
	return loop, nil
}

// detachLoopsBackedBy clears loop devices still bound to image, which only
// happens when a process died between attaching and mounting.
func detachLoopsBackedBy(image string) {
	devices, err := filepath.Glob("/sys/block/loop*/loop/backing_file")
	if err != nil {
		return
	}
	for _, path := range devices {
		file, err := os.Open(path) // #nosec G304 -- sysfs path from the glob.
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		backing := ""
		if scanner.Scan() {
			backing = strings.TrimSuffix(scanner.Text(), " (deleted)")
		}
		_ = file.Close()
		if backing != image {
			continue
		}
		name := filepath.Base(filepath.Dir(filepath.Dir(path)))
		loop, err := os.OpenFile("/dev/"+name, os.O_RDWR, 0) // #nosec G304 -- loop device path.
		if err != nil {
			continue
		}
		_, _, _ = unix.Syscall(unix.SYS_IOCTL, loop.Fd(), loopClearFd, 0)
		_ = loop.Close()
	}
}

func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}
