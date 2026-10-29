package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"go.uber.org/mock/gomock"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/observability/logging"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxservice "github.com/bpradana/sandboxd/internal/service/sandbox"
)

func newApp(t *testing.T) (*fiber.App, *sandboxservice.MockService) {
	t.Helper()

	ctrl := gomock.NewController(t)
	service := sandboxservice.NewMockService(ctrl)
	handler := NewHandler(service, logging.New(zap.NewNop()))
	app := fiber.New()
	app.Post("/sandboxes", handler.CreateSandbox)
	app.Get("/sandboxes", handler.ListSandboxes)
	app.Get("/sandboxes/:id", handler.GetSandbox)
	app.Delete("/sandboxes/:id", handler.DeleteSandbox)
	app.Post("/sandboxes/:id/exec", handler.ExecCommand)
	app.Post("/sandboxes/:id/exec/stream", handler.ExecCommandStream)
	app.Get("/sandboxes/:id/files", handler.DownloadFile)
	app.Put("/sandboxes/:id/files", handler.UploadFile)
	app.Post("/sandboxes/:id/sessions/:session_id/exec", handler.ExecSession)
	app.Post("/sandboxes/:id/terminal/tickets", handler.CreateTerminalTicket)
	app.Get("/sandboxes/:id/terminal", handler.OpenTerminal)
	return app, service
}

type envelope struct {
	Code      int             `json:"code"`
	Entity    string          `json:"entity"`
	ErrorCode string          `json:"error_code"`
	Data      json.RawMessage `json:"data"`
}

func do(t *testing.T, app *fiber.App, method, target, body string) (*http.Response, envelope) {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != "" {
		req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })

	raw, _ := io.ReadAll(res.Body)
	res.Body = io.NopCloser(bytes.NewReader(raw))
	var payload envelope
	_ = json.Unmarshal(raw, &payload)
	return res, payload
}

func TestCreateSandboxReturnsCreated(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Create(gomock.Any(), presentation.CreateSandboxRequest{Image: "alpine", MemoryMB: 256}).
		Return(&presentation.SandboxResponse{ID: "sbx-1", State: "running", CreatedAt: time.Now()}, nil)

	res, payload := do(t, app, http.MethodPost, "/sandboxes", `{"image":"alpine","memory_mb":256}`)
	if res.StatusCode != http.StatusCreated || payload.Entity != sandboxEntity {
		t.Fatalf("status=%d payload=%+v", res.StatusCode, payload)
	}
	var data presentation.SandboxResponse
	_ = json.Unmarshal(payload.Data, &data)
	if data.ID != "sbx-1" {
		t.Fatalf("data = %+v", data)
	}
}

func TestCreateSandboxAcceptsEmptyBody(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Create(gomock.Any(), presentation.CreateSandboxRequest{}).Return(&presentation.SandboxResponse{ID: "sbx-1"}, nil)

	res, _ := do(t, app, http.MethodPost, "/sandboxes", "")
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestCreateSandboxValidatesBody(t *testing.T) {
	t.Parallel()

	app, _ := newApp(t)
	for _, body := range []string{`{"network":"host"}`, `{"memory_mb":1}`, `{"cpu_millis":-5}`, `{not json`} {
		res, payload := do(t, app, http.MethodPost, "/sandboxes", body)
		if res.StatusCode != http.StatusBadRequest || payload.ErrorCode != appctx.CodeValidation {
			t.Errorf("body %s: status=%d payload=%+v", body, res.StatusCode, payload)
		}
	}
}

func TestServiceErrorsKeepTheirStatus(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Get(gomock.Any(), "missing").Return(nil, appctx.ErrSandboxNotFound())

	res, payload := do(t, app, http.MethodGet, "/sandboxes/missing", "")
	if res.StatusCode != http.StatusNotFound || payload.ErrorCode != appctx.CodeSandboxNotFound {
		t.Fatalf("status=%d payload=%+v", res.StatusCode, payload)
	}
}

func TestListSandboxesRejectsUnknownState(t *testing.T) {
	t.Parallel()

	app, _ := newApp(t)
	res, _ := do(t, app, http.MethodGet, "/sandboxes?state=zombie", "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestExecRequiresExactlyOneOfCommandAndArgv(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	for _, body := range []string{`{}`, `{"command":"ls","argv":["ls"]}`, `{"argv":[]}`} {
		res, _ := do(t, app, http.MethodPost, "/sandboxes/sbx-1/exec", body)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s: status = %d", body, res.StatusCode)
		}
	}

	service.EXPECT().Exec(gomock.Any(), "sbx-1", presentation.ExecRequest{Argv: []string{"ls", "-la"}}).
		Return(&presentation.ExecResponse{ExitCode: 0, Stdout: "ok"}, nil)
	res, payload := do(t, app, http.MethodPost, "/sandboxes/sbx-1/exec", `{"argv":["ls","-la"]}`)
	if res.StatusCode != http.StatusOK || payload.Entity != execEntity {
		t.Fatalf("status=%d payload=%+v", res.StatusCode, payload)
	}
}

func TestExecStreamEmitsServerSentEvents(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Stats(gomock.Any(), "sbx-1").Return(&presentation.SandboxStatsResponse{}, nil)
	service.EXPECT().ExecStream(gomock.Any(), "sbx-1", gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, _ presentation.ExecRequest, emit sandboxservice.StreamEmitter) error {
			if err := emit("stdout", presentation.ExecStreamEvent{Data: "hello\n"}); err != nil {
				return err
			}
			return emit("exit", presentation.ExecStreamEvent{Result: &presentation.ExecResponse{ExitCode: 0}})
		})

	res, _ := do(t, app, http.MethodPost, "/sandboxes/sbx-1/exec/stream", `{"command":"echo hello"}`)
	body, _ := io.ReadAll(res.Body)
	if res.Header.Get(fiber.HeaderContentType) != "text/event-stream" {
		t.Fatalf("content type = %q", res.Header.Get(fiber.HeaderContentType))
	}
	want := "event: stdout\ndata: {\"data\":\"hello\\n\"}\n\nevent: exit\n"
	if !strings.HasPrefix(string(body), want) {
		t.Fatalf("body = %q", body)
	}
}

func TestExecStreamFailsFastAsJSON(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Stats(gomock.Any(), "sbx-1").Return(nil, appctx.ErrSandboxNotRunning())
	res, payload := do(t, app, http.MethodPost, "/sandboxes/sbx-1/exec/stream", `{"command":"true"}`)
	if res.StatusCode != http.StatusConflict || payload.ErrorCode != appctx.CodeSandboxNotRunning {
		t.Fatalf("status=%d payload=%+v", res.StatusCode, payload)
	}
}

func TestUploadPassesRawBody(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().WriteFile(gomock.Any(), "sbx-1", presentation.WriteFileRequest{Path: "/home/sandbox/a.txt", Mode: "0600"}, gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, _ presentation.WriteFileRequest, body io.Reader) (*presentation.FileInfoResponse, error) {
			data, _ := io.ReadAll(body)
			if string(data) != "raw bytes" {
				t.Errorf("body = %q", data)
			}
			return &presentation.FileInfoResponse{Path: "/home/sandbox/a.txt", Size: 9}, nil
		})

	req := httptest.NewRequest(http.MethodPut, "/sandboxes/sbx-1/files?path=/home/sandbox/a.txt&mode=0600", strings.NewReader("raw bytes"))
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestUploadRequiresPath(t *testing.T) {
	t.Parallel()

	app, _ := newApp(t)
	req := httptest.NewRequest(http.MethodPut, "/sandboxes/sbx-1/files", strings.NewReader("x"))
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestDownloadStreamsFile(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().OpenFile(gomock.Any(), "sbx-1", presentation.FilePathRequest{Path: "/data/report.json"}).Return(
		io.NopCloser(strings.NewReader(`{"ok":true}`)),
		&presentation.FileInfoResponse{Name: "report.json", Size: 11, Mode: "0644", ModifiedAt: time.Now()},
		nil,
	)

	res, _ := do(t, app, http.MethodGet, "/sandboxes/sbx-1/files?path=/data/report.json", "")
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || string(body) != `{"ok":true}` {
		t.Fatalf("status=%d body=%q", res.StatusCode, body)
	}
	if got := res.Header.Get(fiber.HeaderContentType); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	if got := res.Header.Get(fiber.HeaderContentDisposition); got != `attachment; filename=report.json` {
		t.Fatalf("content disposition = %q", got)
	}
}

func TestSessionExecRequiresCommand(t *testing.T) {
	t.Parallel()

	app, _ := newApp(t)
	res, _ := do(t, app, http.MethodPost, "/sandboxes/sbx-1/sessions/ses-1/exec", `{"timeout_seconds":5}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestCreateTerminalTicketReturnsCreated(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().CreateTerminalTicket(gomock.Any(), "sbx-1").
		Return(&presentation.TerminalTicketResponse{Ticket: strings.Repeat("a", 64), ExpiresAt: time.Now()}, nil)

	res, payload := do(t, app, http.MethodPost, "/sandboxes/sbx-1/terminal/tickets", "")
	if res.StatusCode != http.StatusCreated || payload.Entity != "terminal" || !strings.Contains(string(payload.Data), strings.Repeat("a", 64)) {
		t.Fatalf("status %d, payload %+v", res.StatusCode, payload)
	}
}

func upgradeRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set(fiber.HeaderConnection, "Upgrade")
	req.Header.Set(fiber.HeaderUpgrade, "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	return req
}

func TestOpenTerminalFailsAsJSONBeforeUpgrade(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)

	res, err := app.Test(httptest.NewRequest(http.MethodGet, "/sandboxes/sbx-1/terminal", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("plain GET: status %d", res.StatusCode)
	}

	res, err = app.Test(upgradeRequest("/sandboxes/sbx-1/terminal?cols=0&shell=zsh"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid query: status %d", res.StatusCode)
	}

	service.EXPECT().Get(gomock.Any(), "sbx-gone").Return(nil, appctx.ErrSandboxNotFound())
	res, err = app.Test(upgradeRequest("/sandboxes/sbx-gone/terminal"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("missing sandbox: status %d", res.StatusCode)
	}

	service.EXPECT().Get(gomock.Any(), "sbx-stopped").Return(&presentation.SandboxResponse{ID: "sbx-stopped", State: "stopped"}, nil)
	res, err = app.Test(upgradeRequest("/sandboxes/sbx-stopped/terminal"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Errorf("stopped sandbox: status %d", res.StatusCode)
	}
}

func TestJSONBodyIsReadWithoutContentType(t *testing.T) {
	t.Parallel()

	app, service := newApp(t)
	service.EXPECT().Create(gomock.Any(), presentation.CreateSandboxRequest{Network: "egress"}).
		Return(&presentation.SandboxResponse{ID: "sbx-1", State: "running", CreatedAt: time.Now()}, nil)

	// What `curl -d` sends: a JSON body labeled as a form.
	req := httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(`{"network": "egress"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationForm)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", res.StatusCode)
	}

	req = httptest.NewRequest(http.MethodPost, "/sandboxes", strings.NewReader(`network=egress`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationForm)
	res, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-JSON body: status %d, want 400", res.StatusCode)
	}
}
