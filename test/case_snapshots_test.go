//go:build e2e && linux

package e2e

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// TestSnapshotCloudRoundTrip starts its own daemon and S3-compatible HTTP
// fixture. It needs a privileged container, the Alpine image, and a real
// filesystem under /var/lib/sandboxd, like the rest of the end-to-end suite.
func TestSnapshotCloudRoundTrip(t *testing.T) {
	if os.Getenv("SANDBOXD_E2E_SNAPSHOTS") != "1" {
		t.Skip("set SANDBOXD_E2E_SNAPSHOTS=1 in the privileged dev container")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	cloud := snapshotCloudFixture(t)
	t.Cleanup(cloud.Close)
	work := t.TempDir()
	binary := filepath.Join(work, "sandboxdctl")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/cli")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build daemon: %v\n%s", err, output)
	}
	stateDir, err := os.MkdirTemp("/var/lib/sandboxd", "snapshot-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	// Sandbox root is an unprivileged host uid and must traverse this parent.
	if err := os.Chmod(stateDir, 0o711); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	logs, err := os.Create(filepath.Join(work, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logs.Close() })
	var daemon *exec.Cmd
	stop := func() {
		if daemon == nil {
			return
		}
		_ = daemon.Process.Signal(syscall.SIGTERM)
		if err := daemon.Wait(); err != nil {
			t.Errorf("daemon shutdown: %v", err)
		}
		daemon = nil
	}
	t.Cleanup(func() {
		stop()
		if t.Failed() {
			data, _ := os.ReadFile(logs.Name())
			t.Logf("daemon logs:\n%s", data)
		}
	})
	start := func(imagesDir string) {
		daemon = exec.CommandContext(ctx, binary, "start")
		daemon.Stdout, daemon.Stderr = logs, logs
		daemon.Env = append(os.Environ(),
			"APP_ENV=test", "APP_API_KEYS=", "HTTP_HOST=127.0.0.1", fmt.Sprintf("HTTP_PORT=%d", port),
			"SANDBOX_STATE_DIR="+stateDir, "SANDBOX_IMAGES_DIR="+imagesDir, "SANDBOX_STORAGE_DRIVER=tmpfs",
			"SANDBOX_NETWORK_ENABLED=false", "SANDBOX_DEFAULT_STORAGE_MB=64", "SANDBOX_REAPER_INTERVAL=1s",
			"SNAPSHOT_ENABLED=false", "SNAPSHOT_S3_BUCKET=", "SNAPSHOT_TIMEOUT=1m",
			"SNAPSHOT_SETTINGS_DIR="+filepath.Join(stateDir, "cloud-storage"), "SNAPSHOT_TEMP_DIR="+filepath.Join(stateDir, "snapshot-tmp"),
			"SNAPSHOT_MAX_BYTES=33554432", "AWS_ACCESS_KEY_ID=snapshot-test", "AWS_SECRET_ACCESS_KEY=snapshot-test-secret", // pragma: allowlist secret -- local fixture only.
			"AWS_EC2_METADATA_DISABLED=true")
		if err := daemon.Start(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(envBaseURL, fmt.Sprintf("http://127.0.0.1:%d", port))
	t.Setenv(envAPIKey, "")
	start("/var/lib/sandboxd/images")
	s := newE2ESuite(t)
	connection := presentation.CloudStorageRequest{AuthMode: "access_keys", Bucket: "backups", Region: "us-east-1", Endpoint: cloud.URL, PathStyle: true, Prefix: "sandboxd/snapshots", AccessKeyID: "snapshot-ui-key", SecretAccessKey: "snapshot-ui-secret"} // pragma: allowlist secret -- local fixture credentials.
	status, envelope := s.call(t, http.MethodPost, "/api/v1/settings/cloud-storage/test", connection)
	requireStatus(t, status, http.StatusOK, envelope)
	status, envelope = s.call(t, http.MethodPut, "/api/v1/settings/cloud-storage", connection)
	requireStatus(t, status, http.StatusOK, envelope)
	if strings.Contains(string(envelope.Data), connection.AccessKeyID) || strings.Contains(string(envelope.Data), connection.SecretAccessKey) {
		t.Fatal("credentials returned in settings API")
	}
	stored, err := os.ReadFile(filepath.Join(stateDir, "cloud-storage", "connection.enc"))
	if err != nil || strings.Contains(string(stored), connection.AccessKeyID) || strings.Contains(string(stored), connection.SecretAccessKey) {
		t.Fatal("cloud credentials were not stored encrypted")
	}
	source := s.createSandbox(t, presentation.CreateSandboxRequest{Env: map[string]string{"SNAPSHOT_SAVED": "yes"}, Labels: map[string]string{"task": "snapshot"}, TTLSeconds: 120})
	s.mustSucceed(t, source.ID, "mkdir -p /workspace; echo persisted > /workspace/data; chmod 640 /workspace/data; chown 1000:1000 /workspace/data; ln /workspace/data /workspace/hardlink; ln -s /workspace/data /workspace/link; rm /etc/alpine-release")
	status, envelope = s.call(t, http.MethodPost, "/api/v1/sandboxes/"+source.ID+"/snapshots", presentation.CreateSnapshotRequest{Labels: map[string]string{"backup": "e2e"}})
	requireStatus(t, status, http.StatusAccepted, envelope)
	saved := decode[presentation.SnapshotResponse](t, envelope)
	deadline := time.Now().Add(time.Minute)
	for {
		status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots/"+saved.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		saved = decode[presentation.SnapshotResponse](t, envelope)
		if saved.Status == "ready" {
			break
		}
		if saved.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("snapshot did not finish: %+v", saved)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Restart with an empty image store: restore must be entirely cloud-backed.
	stop()
	start(filepath.Join(stateDir, "empty-images"))
	s = newE2ESuite(t)
	status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots", nil)
	requireStatus(t, status, http.StatusOK, envelope)
	if items := decode[[]presentation.SnapshotResponse](t, envelope); len(items) != 1 || items[0].ID != saved.ID {
		t.Fatalf("snapshot lost across restart: %+v", items)
	}
	restored := s.createSandbox(t, presentation.CreateSandboxRequest{SnapshotID: saved.ID, MemoryMB: 256, TTLSeconds: 60})
	output := s.mustSucceed(t, restored.ID, "test ! -e /etc/alpine-release && test /workspace/data -ef /workspace/hardlink && test \"$(readlink /workspace/link)\" = /workspace/data && test \"$(stat -c '%u:%g:%a' /workspace/data)\" = 1000:1000:640 && cat /workspace/data && printf '%s' \"$SNAPSHOT_SAVED\"")
	if strings.TrimSpace(output) != "persisted\nyes" {
		t.Fatalf("restored filesystem/environment: %q", output)
	}
	status, envelope = s.call(t, http.MethodDelete, "/api/v1/snapshots/"+saved.ID, nil)
	requireStatus(t, status, http.StatusOK, envelope)
	status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots/"+saved.ID, nil)
	requireStatus(t, status, http.StatusNotFound, envelope)
	status, envelope = s.call(t, http.MethodDelete, "/api/v1/settings/cloud-storage", nil)
	requireStatus(t, status, http.StatusOK, envelope)
	status, envelope = s.call(t, http.MethodGet, "/api/v1/snapshots", nil)
	requireStatus(t, status, http.StatusServiceUnavailable, envelope)
}

func snapshotCloudFixture(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=snapshot-ui-key/") {
			t.Error("saved connection credentials were not used")
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			type entry struct {
				Key string `xml:"Key"`
			}
			result := struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []entry  `xml:"Contents"`
			}{}
			for key := range objects {
				result.Contents = append(result.Contents, entry{Key: strings.TrimPrefix(key, "/backups/")})
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(result)
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			objects[r.URL.Path] = data
		case http.MethodGet:
			data, ok := objects[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}
