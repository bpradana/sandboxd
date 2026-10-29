//go:build !linux

package engine

import (
	"context"
	"io"
	"os"
	"runtime"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// unsupported lets the API boot on non-Linux development machines: every
// operation fails with ErrUnsupportedPlatform and readiness reports unhealthy.
type unsupported struct {
	logger *logging.Logger
	exits  chan Exit
}

func New(logger *logging.Logger, _ config.SandboxConfig, _ config.ExecConfig, _ config.NetworkConfig) Engine {
	return &unsupported{logger: logger, exits: make(chan Exit)}
}

func (u *unsupported) Start(context.Context) error {
	u.logger.Warn("sandbox engine unavailable on this platform", zap.String("goos", runtime.GOOS))
	return nil
}

func (u *unsupported) Stop(context.Context) error {
	close(u.exits)
	return nil
}

func (u *unsupported) Ready() error { return ErrUnsupportedPlatform }

func (u *unsupported) Exits() <-chan Exit { return u.exits }

func (u *unsupported) Images(context.Context) ([]Image, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Create(context.Context, Spec) (*Info, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Destroy(context.Context, string) error { return ErrUnsupportedPlatform }

func (u *unsupported) Stats(context.Context, string) (*Stats, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Exec(context.Context, string, ExecRequest, OutputFunc) (*ExecResult, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) OpenSession(context.Context, string, string, SessionRequest) error {
	return ErrUnsupportedPlatform
}

func (u *unsupported) SessionExec(context.Context, string, string, SessionExecRequest, OutputFunc) (*ExecResult, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) CloseSession(context.Context, string, string) error {
	return ErrUnsupportedPlatform
}

func (u *unsupported) Terminal(context.Context, string, TerminalRequest, <-chan TerminalInput, TerminalOutputFunc) (*ExecResult, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) OpenFile(context.Context, string, string) (io.ReadCloser, *FileInfo, error) {
	return nil, nil, ErrUnsupportedPlatform
}

func (u *unsupported) WriteFile(context.Context, string, string, os.FileMode, io.Reader, int64) (*FileInfo, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) ListDir(context.Context, string, string) ([]FileInfo, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Stat(context.Context, string, string) (*FileInfo, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Mkdir(context.Context, string, string, os.FileMode, bool) (*FileInfo, error) {
	return nil, ErrUnsupportedPlatform
}

func (u *unsupported) Remove(context.Context, string, string, bool) error {
	return ErrUnsupportedPlatform
}
