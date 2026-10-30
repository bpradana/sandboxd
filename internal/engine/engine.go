// Package engine isolates sandboxes directly on Linux kernel primitives --
// user/mount/pid/net/uts/ipc/cgroup namespaces, cgroups v2, overlayfs on a
// size-capped tmpfs, seccomp, and capability dropping -- without any external
// container runtime. The platform-neutral contract lives here; the Linux
// implementation lives in engine_linux.go and its sub-packages.
package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/bpradana/sandboxd/internal/model"
)

var (
	ErrCheckpointThaw      = errors.New("sandbox could not resume after checkpoint")
	ErrUnsupportedPlatform = errors.New("sandbox engine requires linux")
	ErrNotReady            = errors.New("sandbox engine is not ready")
	ErrSandboxNotFound     = errors.New("sandbox not found")
	ErrImageNotFound       = errors.New("image not found")
	ErrSessionNotFound     = errors.New("session not found")
	ErrSessionBusy         = errors.New("session is busy")
	ErrSessionClosed       = errors.New("session is closed")
	ErrSessionLimit        = errors.New("too many sessions")
	ErrExecLimit           = errors.New("too many concurrent commands")
	ErrTerminalLimit       = errors.New("too many terminals")
	ErrTerminalNotFound    = errors.New("terminal not found")
	ErrTerminalTaken       = errors.New("terminal attached elsewhere")
	// ErrTerminalDetached ends Terminal when the caller closed input: the
	// terminal keeps running and can be reattached.
	ErrTerminalDetached  = errors.New("terminal detached")
	ErrPathNotFound      = errors.New("path not found")
	ErrInvalidPath       = errors.New("invalid path")
	ErrIsDirectory       = errors.New("path is a directory")
	ErrNotDirectory      = errors.New("path is not a directory")
	ErrExists            = errors.New("path already exists")
	ErrDirectoryNotEmpty = errors.New("directory is not empty")
	ErrFileTooLarge      = errors.New("file too large")
	ErrNoSpace           = errors.New("sandbox storage is full")
)

// Spec describes a sandbox to create. Limits and Network are already
// defaulted and validated by the service.
type Spec struct {
	ID       string
	Image    string
	Hostname string
	Network  string
	Limits   model.Limits
	Env      map[string]string
	// SnapshotArchive is a verified, daemon-owned full filesystem archive.
	// It is imported into a private lower layer instead of resolving Image.
	SnapshotArchive  string
	SnapshotMaxBytes int64
}

// Info reports what the engine allocated for a new sandbox.
type Info struct {
	IPAddress string
	InitPID   int
}

type Stream int

const (
	Stdout Stream = 1
	Stderr Stream = 2
)

func (s Stream) String() string {
	if s == Stderr {
		return "stderr"
	}
	return "stdout"
}

// OutputFunc receives command output chunks as they arrive. Returning an
// error aborts the command (its process group is killed).
type OutputFunc func(stream Stream, data []byte) error

type ExecRequest struct {
	// Argv is executed directly; wrap in /bin/sh -c for shell syntax.
	Argv []string
	// User is consts.UserRoot or consts.UserSandbox (the default).
	User    string
	Env     map[string]string
	Cwd     string
	Stdin   []byte
	Timeout time.Duration
}

type ExecResult struct {
	ExitCode int
	Signal   string
	TimedOut bool
	Duration time.Duration
}

type SessionRequest struct {
	Shell string
	User  string
	Cwd   string
	Env   map[string]string
}

type SessionExecRequest struct {
	Command string
	Timeout time.Duration
}

// TerminalRequest opens a new terminal, an interactive command on a
// pseudo-terminal, under the caller-chosen ID, or with Attach reattaches to
// a running one (Argv, Env, and Cwd are then ignored). An empty Argv starts
// a login shell (bash when the image has it, else sh).
type TerminalRequest struct {
	ID     string
	Attach bool
	User   string
	Argv   []string
	Env    map[string]string
	Cwd    string
	Cols   uint16
	Rows   uint16
	// Ready, if set, runs once the terminal is attached, before any output.
	Ready func() error
}

// TerminalInput is keyboard input (Data), a window resize (Cols and Rows),
// or, with Close, a request to end the terminal.
type TerminalInput struct {
	Data  []byte
	Cols  uint16
	Rows  uint16
	Close bool
}

// TerminalOutputFunc receives terminal output as it arrives. Returning an
// error ends the terminal (its process group is killed).
type TerminalOutputFunc func(data []byte) error

type FileInfo struct {
	Path       string
	Name       string
	Type       string
	Size       int64
	Mode       os.FileMode
	ModTime    time.Time
	LinkTarget string
}

type Stats struct {
	MemoryBytes       int64
	MemoryPeakBytes   int64
	MemoryLimitBytes  int64
	CPUUsageMicros    int64
	PidsCurrent       int64
	PidsLimit         int64
	OOMKills          int64
	StorageUsedBytes  int64
	StorageLimitBytes int64
}

type Image struct {
	Name      string
	CreatedAt time.Time
}

// Exit reports that a sandbox's init process ended without the engine being
// asked to destroy it (crash, OOM of init, kill from outside).
type Exit struct {
	SandboxID string
	Err       error
}

// Engine owns every kernel-level resource of every sandbox. All methods are
// safe for concurrent use. Sandbox IDs are chosen by the caller.
type Engine interface {
	// Start runs preflight checks, removes leftovers of a previous process,
	// and prepares shared host state (cgroup root, bridge, nftables).
	Start(ctx context.Context) error
	// Stop destroys every sandbox and releases shared host state.
	Stop(ctx context.Context) error
	// Ready reports whether the engine can create sandboxes.
	Ready() error
	// Exits delivers unexpected init exits; the channel is closed by Stop.
	Exits() <-chan Exit

	Images(ctx context.Context) ([]Image, error)

	Create(ctx context.Context, spec Spec) (*Info, error)
	Destroy(ctx context.Context, id string) error
	// CaptureSnapshot stops every process and writes a full gzip tar archive.
	// Resources remain allocated until Destroy, including on capture failure.
	CaptureSnapshot(ctx context.Context, id string, dst io.Writer, maxBytes int64) error
	CaptureCheckpoint(ctx context.Context, id string, dst io.Writer, maxBytes int64) error
	// DialSandbox connects only to IPv4 loopback inside this sandbox's network namespace.
	DialSandbox(ctx context.Context, id string, port int) (net.Conn, error)
	Stats(ctx context.Context, id string) (*Stats, error)

	Exec(ctx context.Context, id string, req ExecRequest, out OutputFunc) (*ExecResult, error)
	OpenSession(ctx context.Context, id, sessionID string, req SessionRequest) error
	SessionExec(ctx context.Context, id, sessionID string, req SessionExecRequest, out OutputFunc) (*ExecResult, error)
	CloseSession(ctx context.Context, id, sessionID string) error
	// Terminal relays a terminal until its command exits (returning the
	// result), input is closed (ErrTerminalDetached: the terminal keeps
	// running for the detach window), or ctx ends or out fails (also a
	// detach). A Close input kills the terminal's process group.
	Terminal(ctx context.Context, id string, req TerminalRequest, input <-chan TerminalInput, out TerminalOutputFunc) (*ExecResult, error)

	OpenFile(ctx context.Context, id, path string) (io.ReadCloser, *FileInfo, error)
	WriteFile(ctx context.Context, id, path string, mode os.FileMode, r io.Reader, maxBytes int64) (*FileInfo, error)
	ListDir(ctx context.Context, id, path string) ([]FileInfo, error)
	Stat(ctx context.Context, id, path string) (*FileInfo, error)
	Mkdir(ctx context.Context, id, path string, mode os.FileMode, parents bool) (*FileInfo, error)
	Remove(ctx context.Context, id, path string, recursive bool) error
}
