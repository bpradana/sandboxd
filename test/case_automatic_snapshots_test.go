//go:build e2e && linux

package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/model"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/preview"
)

func (s *e2eSuite) testAutomaticBackupsAndPreview(t *testing.T, previewPort int) {
	t.Helper()
	source := s.createSandbox(t, presentation.CreateSandboxRequest{TTLSeconds: 80, IdleTimeoutSeconds: 120, SnapshotPolicy: &model.SnapshotPolicy{IntervalSeconds: 60, BeforeExpiry: true, Retain: 1}})
	status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+source.ID+"/sessions", presentation.CreateSessionRequest{})
	requireStatus(t, status, http.StatusCreated, envelope)
	session := decode[presentation.SessionResponse](t, envelope)
	sessionPath := "/api/v1/sandboxes/" + source.ID + "/sessions/" + session.ID + "/exec"
	status, envelope = s.call(t, http.MethodPost, sessionPath, presentation.ExecRequest{Command: "export CHECKPOINT_ALIVE=yes", TimeoutSeconds: 10})
	requireStatus(t, status, http.StatusOK, envelope)
	// The minimal Alpine image does not include an HTTP server. Build a tiny
	// static fixture instead of installing packages or requiring internet.
	work := t.TempDir()
	appSource := filepath.Join(work, "main.go")
	if err := os.WriteFile(appSource, []byte(`package main
import "net/http"
func main(){http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){_,_=w.Write([]byte("preview-alive"))});if err:=http.ListenAndServe("127.0.0.1:3000",nil);err!=nil{panic(err)}}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-ldflags=-s -w", "-o", filepath.Join(work, "preview-server"), appSource)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build preview app: %v: %s", err, output)
	}
	appBinary, err := os.ReadFile(filepath.Join(work, "preview-server"))
	if err != nil {
		t.Fatal(err)
	}
	status, body := s.raw(t, http.MethodPut, "/api/v1/sandboxes/"+source.ID+"/files?path=/tmp/preview-server&mode=0755", appBinary, s.apiKey)
	if status != http.StatusOK {
		t.Fatalf("upload preview app: %d: %s", status, body)
	}
	status, envelope = s.call(t, http.MethodPost, sessionPath, presentation.ExecRequest{Command: "/tmp/preview-server >/tmp/preview-app.log 2>&1 </dev/null & sleep 0.2", TimeoutSeconds: 10})
	requireStatus(t, status, http.StatusOK, envelope)
	status, envelope = s.call(t, http.MethodPost, "/api/v1/sandboxes/"+source.ID+"/previews", presentation.CreatePreviewRequest{Port: 3000})
	requireStatus(t, status, http.StatusCreated, envelope)
	app := decode[preview.Session](t, envelope)
	// Preview scripts must not mutate even an unauthenticated dev API through
	// browser simple requests whose responses CORS would merely hide.
	csrf, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.baseURL+"/api/v1/sandboxes", strings.NewReader("{}"))
	csrf.Header.Set("Origin", strings.TrimSuffix(app.URL, "/"))
	csrf.Header.Set("Content-Type", "text/plain")
	csrfResponse, err := s.client.Do(csrf)
	if err != nil {
		t.Fatal(err)
	}
	_ = csrfResponse.Body.Close()
	if csrfResponse.StatusCode != http.StatusForbidden {
		t.Fatal("preview origin can mutate the API")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fmt.Sprintf("127.0.0.1:%d", previewPort))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	checkPreview := func(want int) {
		res, err := client.Get(app.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != want || want == 200 && !strings.Contains(string(body), "preview-alive") {
			if want == 200 {
				t.Logf("app diagnostics: %+v", s.exec(t, source.ID, "cat /tmp/preview-app.log; wget -q -T 2 -O- http://127.0.0.1:3000/; ps", 10))
			}
			t.Fatalf("preview returned %d: %s", res.StatusCode, body)
		}
	}
	checkPreview(http.StatusOK)
	// A checkpoint must preserve the existing shell and HTTP process, and a
	// failed bounded capture must thaw them too.
	s.mustSucceed(t, source.ID, "dd if=/dev/zero of=/tmp/too-large bs=1M count=34 2>/dev/null")
	status, envelope = s.call(t, http.MethodPost, "/api/v1/sandboxes/"+source.ID+"/snapshots", presentation.CreateSnapshotRequest{KeepRunning: true})
	requireStatus(t, status, http.StatusAccepted, envelope)
	failed := decode[presentation.SnapshotResponse](t, envelope)
	s.waitBackup(t, failed.ID, "failed")
	checkPreview(http.StatusOK)
	s.mustSucceed(t, source.ID, "rm /tmp/too-large")
	status, envelope = s.call(t, http.MethodDelete, "/api/v1/snapshots/"+failed.ID, nil)
	requireStatus(t, status, http.StatusOK, envelope)
	status, envelope = s.call(t, http.MethodPost, "/api/v1/sandboxes/"+source.ID+"/snapshots", presentation.CreateSnapshotRequest{KeepRunning: true})
	requireStatus(t, status, http.StatusAccepted, envelope)
	manual := decode[presentation.SnapshotResponse](t, envelope)
	s.waitBackup(t, manual.ID, "ready")
	status, envelope = s.call(t, http.MethodPost, sessionPath, presentation.ExecRequest{Command: "echo $CHECKPOINT_ALIVE", TimeoutSeconds: 10})
	requireStatus(t, status, http.StatusOK, envelope)
	if out := decode[presentation.ExecResponse](t, envelope); out.Stdout != "yes\n" {
		t.Fatalf("checkpoint lost shell state: %+v", out)
	}
	checkPreview(http.StatusOK)
	status, envelope = s.call(t, http.MethodGet, "/api/v1/sandboxes/"+source.ID, nil)
	requireStatus(t, status, http.StatusOK, envelope)
	if sb := decode[presentation.SandboxResponse](t, envelope); sb.State != "running" || sb.SnapshotInProgress {
		t.Fatalf("checkpoint stopped source: %+v", sb)
	}
	deadline := time.Now().Add(90 * time.Second)
	seenScheduled := false
	var final presentation.SnapshotResponse
	for time.Now().Before(deadline) {
		status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots", nil)
		requireStatus(t, status, http.StatusOK, envelope)
		for _, item := range decode[[]presentation.SnapshotResponse](t, envelope) {
			if item.SandboxID != source.ID || item.Status != "ready" {
				continue
			}
			if item.Trigger == "scheduled" {
				seenScheduled = true
			}
			if item.Trigger == "expiry" {
				final = item
			}
		}
		if final.ID != "" {
			status, envelope = s.call(t, http.MethodGet, "/api/v1/sandboxes/"+source.ID, nil)
			requireStatus(t, status, http.StatusOK, envelope)
			if decode[presentation.SandboxResponse](t, envelope).State == "stopped" {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !seenScheduled || final.ID == "" {
		t.Fatalf("scheduled=%v expiry=%+v", seenScheduled, final)
	}
	// Retention keeps the latest automatic backup and leaves the manual one.
	status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots", nil)
	requireStatus(t, status, http.StatusOK, envelope)
	items := decode[[]presentation.SnapshotResponse](t, envelope)
	if len(items) != 2 {
		t.Fatalf("unexpected retention: %+v", items)
	}
	status, envelope = s.call(t, http.MethodGet, "/api/v1/sandboxes/"+source.ID, nil)
	requireStatus(t, status, http.StatusOK, envelope)
	if sb := decode[presentation.SandboxResponse](t, envelope); sb.State != "stopped" {
		t.Fatalf("expiry backup did not stop sandbox: %+v", sb)
	}
	checkPreview(http.StatusGone)
	for _, id := range []string{manual.ID, final.ID} {
		status, envelope = s.call(t, http.MethodDelete, "/api/v1/snapshots/"+id, nil)
		requireStatus(t, status, http.StatusOK, envelope)
	}
}

func (s *e2eSuite) waitBackup(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		status, envelope := s.call(t, http.MethodGet, "/api/v1/snapshots/"+id, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		item := decode[presentation.SnapshotResponse](t, envelope)
		if item.Status == want {
			return
		}
		if item.Status == "failed" || item.Status == "ready" {
			t.Fatalf("wanted %s backup, got %+v", want, item)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("backup %s did not become %s", id, want)
}
