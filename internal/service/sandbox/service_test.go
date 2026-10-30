package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxrepository "github.com/bpradana/sandboxd/internal/repository/sandbox"
)

func testConfigs() (config.SandboxConfig, config.ExecConfig, config.NetworkConfig) {
	sandboxCfg := config.SandboxConfig{
		DefaultImage:     "alpine",
		MaxSandboxes:     2,
		DefaultCPUMillis: 1000, MaxCPUMillis: 2000,
		DefaultMemoryMB: 256, MaxMemoryMB: 1024,
		DefaultStorageMB: 128, MaxStorageMB: 512,
		DefaultPids: 64, MaxPids: 128,
		DefaultTTL: 10 * time.Minute, MaxTTL: time.Hour,
		IdleTimeout:    5 * time.Minute,
		ReaperInterval: time.Second,
		TombstoneTTL:   time.Minute,
		DestroyTimeout: time.Second,
	}
	execCfg := config.ExecConfig{
		DefaultTimeout: 30 * time.Second,
		MaxTimeout:     time.Minute,
		MaxOutputBytes: 8,
		MaxConcurrent:  2,
		MaxSessions:    2,
		MaxTerminals:   2,
		FileMaxBytes:   1024,
	}
	return sandboxCfg, execCfg, config.NetworkConfig{Enabled: false}
}

type fixture struct {
	svc    *service
	repo   sandboxrepository.Repository
	engine *engine.MockEngine
	now    time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctrl := gomock.NewController(t)
	eng := engine.NewMockEngine(ctrl)
	repo := sandboxrepository.NewRepository()
	sandboxCfg, execCfg, netCfg := testConfigs()
	svc := newService(repo, eng, logging.New(zap.NewNop()), nil, sandboxCfg, execCfg, netCfg)
	f := &fixture{svc: svc, repo: repo, engine: eng, now: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)}
	svc.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) running(t *testing.T, id string) {
	t.Helper()
	err := f.repo.Create(context.Background(), &model.Sandbox{
		ID: id, State: consts.SandboxStateRunning, Network: consts.NetworkNone,
		Env:       map[string]string{"FROM_SANDBOX": "1", "SHARED": "sandbox"},
		CreatedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute), LastActivityAt: f.now, IdleTimeout: 5 * time.Minute,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
}

func httpStatus(err error) int {
	var httpErr *appctx.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status()
	}
	return 0
}

func TestCreateAppliesDefaults(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, spec engine.Spec) (*engine.Info, error) {
		if spec.Image != "alpine" || spec.Network != consts.NetworkNone {
			t.Errorf("spec = %+v", spec)
		}
		if spec.Limits != (model.Limits{CPUMillis: 1000, MemoryMB: 256, StorageMB: 128, PidsMax: 64}) {
			t.Errorf("limits = %+v", spec.Limits)
		}
		return &engine.Info{InitPID: 42}, nil
	})

	resp, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if resp.State != consts.SandboxStateRunning || !resp.ExpiresAt.Equal(f.now.Add(10*time.Minute)) {
		t.Fatalf("Create() = %+v", resp)
	}
	if !strings.HasPrefix(resp.ID, "sbx-") {
		t.Fatalf("ID = %q", resp.ID)
	}
}

func TestCreateDoesNotResurrectConcurrentDelete(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().Destroy(gomock.Any(), gomock.Any()).Return(nil).Times(2)
	f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, spec engine.Spec) (*engine.Info, error) {
		if err := f.svc.Destroy(ctx, spec.ID); err != nil {
			t.Fatalf("concurrent delete: %v", err)
		}
		return &engine.Info{InitPID: 42}, nil
	})
	if _, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("creation resurrected a deleted sandbox: %v", err)
	}
	items, err := f.repo.List(context.Background(), sandboxrepository.Filter{})
	if err != nil || len(items) != 1 || items[0].State != consts.SandboxStateStopped || items[0].StoppedAt == nil {
		t.Fatalf("deleted sandbox changed: %+v, %v", items, err)
	}
}

func TestCreateRejectsLimitsAboveMaximum(t *testing.T) {
	t.Parallel()

	cases := []presentation.CreateSandboxRequest{
		{MemoryMB: 2048},
		{CPUMillis: 4000},
		{StorageMB: 1024},
		{PidsMax: 1000},
		{TTLSeconds: 7200},
		{Network: consts.NetworkEgress},
	}
	for _, req := range cases {
		f := newFixture(t)
		f.engine.EXPECT().Ready().Return(nil)
		_, err := f.svc.Create(context.Background(), req)
		if httpStatus(err) != http.StatusBadRequest {
			t.Errorf("Create(%+v) error = %v, want 400", req, err)
		}
	}
}

func TestCreateFailureLeavesFailedTombstone(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.engine.EXPECT().Ready().Return(nil)
	f.engine.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil, engine.ErrImageNotFound)

	_, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{Image: "missing"})
	if httpStatus(err) != http.StatusNotFound {
		t.Fatalf("Create() error = %v, want 404", err)
	}
	list, _ := f.repo.List(context.Background(), sandboxrepository.Filter{})
	if len(list) != 1 || list[0].State != consts.SandboxStateFailed || list[0].StoppedAt == nil {
		t.Fatalf("records = %+v", list)
	}
}

func TestCreateRespectsSandboxCap(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.running(t, "b")
	f.engine.EXPECT().Ready().Return(nil)

	_, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{})
	if httpStatus(err) != http.StatusTooManyRequests {
		t.Fatalf("Create() error = %v, want 429", err)
	}
}

func TestCreateWhenEngineUnavailable(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.engine.EXPECT().Ready().Return(engine.ErrUnsupportedPlatform)
	_, err := f.svc.Create(context.Background(), presentation.CreateSandboxRequest{})
	if httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("Create() error = %v, want 503", err)
	}
}

func TestExecMergesEnvAndCapsOutput(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.engine.EXPECT().Exec(gomock.Any(), "a", gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, req engine.ExecRequest, out engine.OutputFunc) (*engine.ExecResult, error) {
			if strings.Join(req.Argv, " ") != "/bin/sh -c echo hi" {
				t.Errorf("argv = %q", req.Argv)
			}
			if req.Env["FROM_SANDBOX"] != "1" || req.Env["SHARED"] != "request" {
				t.Errorf("env = %v", req.Env)
			}
			if req.Timeout != 30*time.Second {
				t.Errorf("timeout = %v", req.Timeout)
			}
			_ = out(engine.Stdout, []byte("0123456789"))
			_ = out(engine.Stderr, []byte("err"))
			return &engine.ExecResult{ExitCode: 3, Duration: time.Second}, nil
		})

	resp, err := f.svc.Exec(context.Background(), "a", presentation.ExecRequest{
		Command: "echo hi",
		Env:     map[string]string{"SHARED": "request"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if resp.Stdout != "01234567" || !resp.StdoutTruncated || resp.Stderr != "err" || resp.StderrTruncated || resp.ExitCode != 3 {
		t.Fatalf("Exec() = %+v", resp)
	}
}

func TestExecRejectsTimeoutAboveMaximum(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	_, err := f.svc.Exec(context.Background(), "a", presentation.ExecRequest{Command: "true", TimeoutSeconds: 3600})
	if httpStatus(err) != http.StatusBadRequest {
		t.Fatalf("Exec() error = %v, want 400", err)
	}
}

func TestExecRequiresRunningSandbox(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if _, err := f.svc.Exec(context.Background(), "missing", presentation.ExecRequest{Command: "true"}); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("Exec(missing) error = %v, want 404", err)
	}
	_ = f.repo.Create(context.Background(), &model.Sandbox{ID: "creating", State: consts.SandboxStateCreating}, 0)
	if _, err := f.svc.Exec(context.Background(), "creating", presentation.ExecRequest{Command: "true"}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("Exec(creating) error = %v, want 409", err)
	}
}

func TestExecMapsEngineLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.engine.EXPECT().Exec(gomock.Any(), "a", gomock.Any(), gomock.Any()).Return(nil, engine.ErrExecLimit)
	if _, err := f.svc.Exec(context.Background(), "a", presentation.ExecRequest{Argv: []string{"true"}}); httpStatus(err) != http.StatusTooManyRequests {
		t.Fatalf("Exec() error = %v, want 429", err)
	}
}

func TestDestroyIsIdempotent(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.engine.EXPECT().Destroy(gomock.Any(), "a").Return(nil).Times(1)

	if err := f.svc.Destroy(context.Background(), "a"); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if err := f.svc.Destroy(context.Background(), "a"); err != nil {
		t.Fatalf("second Destroy() error = %v", err)
	}
	sb, _ := f.repo.Get(context.Background(), "a")
	if sb.State != consts.SandboxStateStopped || sb.StopReason != consts.StopReasonDeleted {
		t.Fatalf("sandbox = %+v", sb)
	}
}

func TestDestroyFailureMarksFailed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.engine.EXPECT().Destroy(gomock.Any(), "a").Return(errors.New("umount: busy"))

	if err := f.svc.Destroy(context.Background(), "a"); httpStatus(err) != http.StatusInternalServerError {
		t.Fatalf("Destroy() error = %v, want 500", err)
	}
	sb, _ := f.repo.Get(context.Background(), "a")
	if sb.State != consts.SandboxStateFailed || sb.FailureReason == "" {
		t.Fatalf("sandbox = %+v", sb)
	}
}

func TestKeepaliveIsCappedByMaxTTL(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.now = f.now.Add(50 * time.Minute)

	resp, err := f.svc.Keepalive(context.Background(), "a", presentation.KeepaliveRequest{TTLSeconds: 1800})
	if err != nil {
		t.Fatal(err)
	}
	created := f.now.Add(-50 * time.Minute)
	if !resp.ExpiresAt.Equal(created.Add(time.Hour)) {
		t.Fatalf("ExpiresAt = %v, want creation + MaxTTL", resp.ExpiresAt)
	}
}

func TestReapDestroysExpiredAndIdleSandboxes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "expired")
	f.running(t, "idle")
	f.running(t, "fresh")
	ctx := context.Background()
	_, _ = f.repo.Update(ctx, "expired", func(sb *model.Sandbox) error {
		sb.ExpiresAt = f.now.Add(time.Minute)
		sb.LastActivityAt = f.now.Add(2 * time.Minute)
		return nil
	})
	_, _ = f.repo.Update(ctx, "idle", func(sb *model.Sandbox) error {
		sb.ExpiresAt = f.now.Add(time.Hour)
		return nil
	})
	_, _ = f.repo.Update(ctx, "fresh", func(sb *model.Sandbox) error {
		sb.ExpiresAt = f.now.Add(time.Hour)
		sb.LastActivityAt = f.now.Add(5 * time.Minute)
		return nil
	})
	f.now = f.now.Add(6 * time.Minute)

	f.engine.EXPECT().Destroy(gomock.Any(), "expired").Return(nil)
	f.engine.EXPECT().Destroy(gomock.Any(), "idle").Return(nil)
	f.svc.reapOnce(ctx)

	for id, reason := range map[string]string{"expired": consts.StopReasonExpired, "idle": consts.StopReasonIdle} {
		sb, _ := f.repo.Get(ctx, id)
		if sb.State != consts.SandboxStateStopped || sb.StopReason != reason {
			t.Errorf("%s = %s/%s, want stopped/%s", id, sb.State, sb.StopReason, reason)
		}
	}
	if sb, _ := f.repo.Get(ctx, "fresh"); sb.State != consts.SandboxStateRunning {
		t.Errorf("fresh = %s", sb.State)
	}

	f.now = f.now.Add(2 * time.Minute)
	f.svc.reapOnce(ctx)
	if _, err := f.repo.Get(ctx, "expired"); !errors.Is(err, sandboxrepository.ErrNotFound) {
		t.Errorf("tombstone not purged: %v", err)
	}
}

func TestReapSkipsIdleCheckWhileBusy(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	ctx := context.Background()
	_, _ = f.repo.Update(ctx, "a", func(sb *model.Sandbox) error {
		sb.ExpiresAt = f.now.Add(time.Hour)
		return nil
	})
	done := f.svc.beginActivity(ctx, "a")
	f.now = f.now.Add(6 * time.Minute)

	f.svc.reapOnce(ctx) // a Destroy call here would fail the mock
	if sb, _ := f.repo.Get(ctx, "a"); sb.State != consts.SandboxStateRunning {
		t.Fatalf("busy sandbox reaped: %s", sb.State)
	}

	done()
	f.now = f.now.Add(6 * time.Minute)
	f.engine.EXPECT().Destroy(gomock.Any(), "a").Return(nil)
	f.svc.reapOnce(ctx)
	if sb, _ := f.repo.Get(ctx, "a"); sb.StopReason != consts.StopReasonIdle {
		t.Fatalf("idle sandbox not reaped: %+v", sb)
	}
}

func TestHandleExitMarksSandboxFailed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	f.engine.EXPECT().Destroy(gomock.Any(), "a").Return(nil)

	f.svc.handleExit(context.Background(), "a", errors.New("signal: killed"))
	sb, _ := f.repo.Get(context.Background(), "a")
	if sb.State != consts.SandboxStateFailed || sb.StopReason != consts.StopReasonExited || !strings.Contains(sb.FailureReason, "killed") {
		t.Fatalf("sandbox = %+v", sb)
	}
}

func TestSessionExecBusyAndTimeout(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	ctx := context.Background()
	f.engine.EXPECT().OpenSession(gomock.Any(), "a", gomock.Any(), gomock.Any()).Return(nil)
	session, err := f.svc.CreateSession(ctx, "a", presentation.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	_, _ = f.repo.UpdateSession(ctx, "a", session.ID, func(s *model.Session) error {
		s.State = consts.SessionStateBusy
		return nil
	})
	if _, err := f.svc.SessionExec(ctx, "a", session.ID, presentation.SessionExecRequest{Command: "true"}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("SessionExec(busy) error = %v, want 409", err)
	}
	_, _ = f.repo.UpdateSession(ctx, "a", session.ID, func(s *model.Session) error {
		s.State = consts.SessionStateIdle
		return nil
	})

	f.engine.EXPECT().SessionExec(gomock.Any(), "a", session.ID, gomock.Any(), gomock.Any()).
		Return(&engine.ExecResult{ExitCode: 137, TimedOut: true}, nil)
	resp, err := f.svc.SessionExec(ctx, "a", session.ID, presentation.SessionExecRequest{Command: "sleep 99"})
	if err != nil || !resp.TimedOut {
		t.Fatalf("SessionExec() = %+v, %v", resp, err)
	}
	stored, _ := f.repo.GetSession(ctx, "a", session.ID)
	if stored.State != consts.SessionStateTerminated {
		t.Fatalf("session state = %s, want terminated", stored.State)
	}
	if _, err := f.svc.SessionExec(ctx, "a", session.ID, presentation.SessionExecRequest{Command: "true"}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("SessionExec(terminated) error = %v, want 409", err)
	}
}

func TestFileOperationsValidatePaths(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	ctx := context.Background()

	for _, bad := range []string{"", "relative/path", "/nul\x00byte"} {
		if _, err := f.svc.StatFile(ctx, "a", presentation.FilePathRequest{Path: bad}); httpStatus(err) != http.StatusBadRequest {
			t.Errorf("StatFile(%q) error = %v, want 400", bad, err)
		}
	}
	if err := f.svc.RemoveFile(ctx, "a", presentation.RemoveFileRequest{Path: "/../"}); httpStatus(err) != http.StatusBadRequest {
		t.Errorf("RemoveFile(/) error = %v, want 400", err)
	}

	f.engine.EXPECT().Stat(gomock.Any(), "a", "/etc/passwd").Return(&engine.FileInfo{Path: "/etc/passwd", Name: "passwd", Type: consts.FileTypeFile, Mode: 0o644}, nil)
	info, err := f.svc.StatFile(ctx, "a", presentation.FilePathRequest{Path: "/etc/../etc//passwd"})
	if err != nil || info.Mode != "0644" {
		t.Fatalf("StatFile() = %+v, %v", info, err)
	}
}

func TestWriteFileParsesModeAndMapsErrors(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "a")
	ctx := context.Background()

	if _, err := f.svc.WriteFile(ctx, "a", presentation.WriteFileRequest{Path: "/x", Mode: "4755"}, strings.NewReader("")); httpStatus(err) != http.StatusBadRequest {
		t.Fatalf("WriteFile(setuid mode) error = %v, want 400", err)
	}

	f.engine.EXPECT().WriteFile(gomock.Any(), "a", "/x", gomock.Any(), gomock.Any(), int64(1024)).DoAndReturn(
		func(_ context.Context, _, _ string, mode any, r io.Reader, _ int64) (*engine.FileInfo, error) {
			data, _ := io.ReadAll(r)
			if string(data) != "body" {
				t.Errorf("body = %q", data)
			}
			return nil, engine.ErrNoSpace
		})
	if _, err := f.svc.WriteFile(ctx, "a", presentation.WriteFileRequest{Path: "/x", Mode: "0600"}, strings.NewReader("body")); httpStatus(err) != http.StatusInsufficientStorage {
		t.Fatalf("WriteFile() error = %v, want 507", err)
	}
}
