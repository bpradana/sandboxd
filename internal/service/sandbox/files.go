package sandbox

import (
	"context"
	"io"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
)

const (
	defaultFileMode = 0o644
	defaultDirMode  = 0o755
)

func (s *service) OpenFile(ctx context.Context, id string, req presentation.FilePathRequest) (io.ReadCloser, *presentation.FileInfoResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.OpenFile")
	defer span.End()

	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, err
	}
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id), tracing.String(tracing.FilePathKey, filePath))

	reader, info, err := s.engine.OpenFile(ctx, id, filePath)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, mapError(err)
	}
	return reader, toFileInfoResponse(info), nil
}

func (s *service) WriteFile(ctx context.Context, id string, req presentation.WriteFileRequest, body io.Reader) (*presentation.FileInfoResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.WriteFile")
	defer span.End()

	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id), tracing.String(tracing.FilePathKey, filePath))

	mode, err := parseMode(req.Mode, defaultFileMode)
	if err != nil {
		return nil, err
	}
	done := s.beginActivity(ctx, id)
	info, err := s.engine.WriteFile(ctx, id, filePath, mode, body, s.exec.FileMaxBytes)
	done()
	if err != nil {
		tracing.RecordError(span, err)
		return nil, mapError(err)
	}
	return toFileInfoResponse(info), nil
}

func (s *service) ListDir(ctx context.Context, id string, req presentation.FilePathRequest) ([]presentation.FileInfoResponse, error) {
	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		return nil, err
	}
	entries, err := s.engine.ListDir(ctx, id, filePath)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]presentation.FileInfoResponse, 0, len(entries))
	for i := range entries {
		result = append(result, *toFileInfoResponse(&entries[i]))
	}
	return result, nil
}

func (s *service) StatFile(ctx context.Context, id string, req presentation.FilePathRequest) (*presentation.FileInfoResponse, error) {
	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		return nil, err
	}
	info, err := s.engine.Stat(ctx, id, filePath)
	if err != nil {
		return nil, mapError(err)
	}
	return toFileInfoResponse(info), nil
}

func (s *service) Mkdir(ctx context.Context, id string, req presentation.MkdirRequest) (*presentation.FileInfoResponse, error) {
	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		return nil, err
	}
	mode, err := parseMode(req.Mode, defaultDirMode)
	if err != nil {
		return nil, err
	}
	info, err := s.engine.Mkdir(ctx, id, filePath, mode, req.Parents)
	if err != nil {
		return nil, mapError(err)
	}
	return toFileInfoResponse(info), nil
}

func (s *service) RemoveFile(ctx context.Context, id string, req presentation.RemoveFileRequest) error {
	filePath, err := s.prepareFileOp(ctx, id, req.Path)
	if err != nil {
		return err
	}
	if filePath == "/" {
		return appctx.ErrInvalidPath()
	}
	return mapError(s.engine.Remove(ctx, id, filePath, req.Recursive))
}

// prepareFileOp requires a running sandbox, records activity, and normalizes
// the path. Containment is enforced by the engine (openat2 RESOLVE_IN_ROOT);
// this only rejects inputs that can never be valid.
func (s *service) prepareFileOp(ctx context.Context, id, rawPath string) (string, error) {
	if _, err := s.runningSandbox(ctx, id); err != nil {
		return "", err
	}
	s.touch(ctx, id)
	return cleanSandboxPath(rawPath)
}

func cleanSandboxPath(rawPath string) (string, error) {
	if rawPath == "" || !strings.HasPrefix(rawPath, "/") || strings.ContainsRune(rawPath, 0) {
		return "", appctx.ErrInvalidPath()
	}
	return path.Clean(rawPath), nil
}

// parseMode parses an octal permission string. setuid/setgid bits are never
// granted through the API.
func parseMode(raw string, fallback os.FileMode) (os.FileMode, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseUint(raw, 8, 32)
	if err != nil || value > 0o1777 {
		return 0, appctx.ErrLimitExceeded("mode must be an octal permission between 0000 and 1777")
	}
	mode := os.FileMode(value & 0o777)
	if value&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode, nil
}
