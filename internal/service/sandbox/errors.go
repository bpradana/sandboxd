package sandbox

import (
	"errors"
	"net/http"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/engine"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

// mapError turns engine and repository sentinels into client-facing
// HTTPErrors, keeping the original error as the cause for logs and spans.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*appctx.HTTPError](err); ok {
		return err
	}

	var mapped *appctx.HTTPError
	switch {
	case errors.Is(err, snapshot.ErrNotFound):
		mapped = appctx.NewHTTPError(http.StatusNotFound, "snapshot not found").WithErrorCode(appctx.CodeSnapshotNotFound)
	case errors.Is(err, snapshot.ErrInvalidID), errors.Is(err, snapshot.ErrCorrupt):
		mapped = appctx.NewHTTPError(http.StatusBadRequest, "invalid snapshot id or archive").WithErrorCode(appctx.CodeSnapshotInvalid)
	case errors.Is(err, snapshot.ErrTooLarge):
		mapped = appctx.ErrLimitExceeded("snapshot exceeds maximum allowed size")
	case errors.Is(err, engine.ErrSandboxNotFound), errors.Is(err, sandboxrepository.ErrNotFound):
		mapped = appctx.ErrSandboxNotFound()
	case errors.Is(err, engine.ErrImageNotFound):
		mapped = appctx.ErrImageNotFound()
	case errors.Is(err, engine.ErrImageExists):
		mapped = appctx.ErrPathConflict("image already exists")
	case errors.Is(err, engine.ErrInvalidImageName):
		mapped = appctx.ErrLimitExceeded("image name must match [a-z0-9][a-z0-9._-]{0,63}")
	case errors.Is(err, engine.ErrSessionNotFound):
		mapped = appctx.ErrSessionNotFound()
	case errors.Is(err, engine.ErrSessionBusy):
		mapped = appctx.ErrSessionBusy()
	case errors.Is(err, engine.ErrSessionClosed):
		mapped = appctx.ErrSessionClosed()
	case errors.Is(err, engine.ErrSessionLimit):
		mapped = appctx.ErrLimitExceeded("maximum number of sessions reached")
	case errors.Is(err, engine.ErrExecLimit):
		mapped = appctx.ErrExecLimitReached()
	case errors.Is(err, engine.ErrTerminalLimit):
		mapped = appctx.ErrTerminalLimitReached()
	case errors.Is(err, engine.ErrTerminalNotFound):
		mapped = appctx.ErrTerminalNotFound()
	case errors.Is(err, engine.ErrTerminalTaken):
		mapped = appctx.ErrTerminalTaken()
	case errors.Is(err, engine.ErrPathNotFound):
		mapped = appctx.ErrPathNotFound()
	case errors.Is(err, engine.ErrInvalidPath):
		mapped = appctx.ErrInvalidPath()
	case errors.Is(err, engine.ErrIsDirectory):
		mapped = appctx.ErrPathConflict("path is a directory")
	case errors.Is(err, engine.ErrNotDirectory):
		mapped = appctx.ErrPathConflict("path is not a directory")
	case errors.Is(err, engine.ErrExists):
		mapped = appctx.ErrPathConflict("path already exists")
	case errors.Is(err, engine.ErrDirectoryNotEmpty):
		mapped = appctx.ErrPathConflict("directory is not empty")
	case errors.Is(err, engine.ErrFileTooLarge):
		mapped = appctx.ErrFileTooLarge()
	case errors.Is(err, engine.ErrNoSpace):
		mapped = appctx.ErrStorageFull()
	case errors.Is(err, sandboxrepository.ErrLimitReached):
		mapped = appctx.ErrSandboxLimitReached()
	case errors.Is(err, engine.ErrUnsupportedPlatform):
		mapped = appctx.ErrUnsupportedPlatform()
	case errors.Is(err, engine.ErrNotReady):
		mapped = appctx.ErrEngineUnavailable()
	default:
		mapped = appctx.ErrInternal()
	}
	return mapped.WithCause(err)
}
