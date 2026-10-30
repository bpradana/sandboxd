package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/config"
	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/snapshot"
)

type connectionFixture struct {
	mu          sync.Mutex
	objects     map[string][]byte
	denyDelete  bool
	denyList    bool
	credentials []string
}

func newConnectionFixture(t *testing.T) (*httptest.Server, *connectionFixture) {
	t.Helper()
	state := &connectionFixture{objects: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.credentials = append(state.credentials, r.Header.Get("Authorization"))
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			if state.denyList {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>example-secret</Message></Error>")
				return
			}
			_, _ = io.WriteString(w, "<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>")
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			state.objects[r.URL.Path] = data
		case http.MethodGet:
			data, ok := state.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			if state.denyDelete {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(403)
				_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
				return
			}
			delete(state.objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

func cloudFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.svc.snapshotCfg = config.SnapshotConfig{Region: "us-east-1", Prefix: "sandboxd/snapshots", SettingsDir: filepath.Join(t.TempDir(), "cloud"), TempDir: t.TempDir(), Timeout: time.Minute, MaxBytes: 1 << 20, MaxConcurrent: 1}
	f.svc.snapshotSlots = make(chan struct{}, 1)
	if err := f.svc.loadCloudStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func cloudRequest(endpoint string) presentation.CloudStorageRequest {
	return presentation.CloudStorageRequest{AuthMode: snapshot.AuthAccessKeys, Bucket: "backups", Region: "us-east-1", Endpoint: endpoint, Prefix: "sandboxd/snapshots", PathStyle: true, AccessKeyID: "example-key", SecretAccessKey: "example-secret", SessionToken: "example-token"} // pragma: allowlist secret -- local test credentials.
}

func TestCloudStorageTestDoesNotSaveAndSaveSurvivesRestart(t *testing.T) {
	t.Parallel()
	server, state := newConnectionFixture(t)
	f := cloudFixture(t)
	req := cloudRequest(server.URL)
	ctx := context.Background()
	if err := f.svc.TestCloudStorage(ctx, req); err != nil {
		t.Fatal(err)
	}
	if f.svc.snapshots != nil {
		t.Fatal("testing enabled cloud storage")
	}
	if _, err := os.Stat(filepath.Join(f.svc.snapshotCfg.SettingsDir, "connection.enc")); !os.IsNotExist(err) {
		t.Fatal("test persisted credentials")
	}
	response, err := f.svc.SaveCloudStorage(ctx, req)
	if err != nil || !response.Connected || !response.HasSavedCredentials {
		t.Fatalf("save response %+v error %v", response, err)
	}
	encoded, _ := json.Marshal(response)
	disk, err := os.ReadFile(filepath.Join(f.svc.snapshotCfg.SettingsDir, "connection.enc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{req.AccessKeyID, req.SecretAccessKey, req.SessionToken} {
		if bytes.Contains(encoded, []byte(value)) || bytes.Contains(disk, []byte(value)) {
			t.Fatal("credentials exposed in API response or disk")
		}
	}
	state.mu.Lock()
	if len(state.objects) != 0 {
		t.Error("connection test left probe objects behind")
	}
	for _, header := range state.credentials {
		if !strings.Contains(header, "Credential=example-key/") {
			t.Error("explicit credentials were not used")
		}
	}
	state.mu.Unlock()
	restarted := cloudFixture(t)
	restarted.svc.snapshotCfg = f.svc.snapshotCfg
	if err := restarted.svc.loadCloudStorage(ctx); err != nil {
		t.Fatal(err)
	}
	status, _ := restarted.svc.GetCloudStorage(ctx)
	if !status.Connected || !status.HasSavedCredentials || status.Source != "saved" {
		t.Fatal("connection lost on restart")
	}
	reuse := req
	reuse.AccessKeyID, reuse.SecretAccessKey, reuse.SessionToken = "", "", ""
	reuse.UseSavedCredentials = true
	reuse.Prefix = "other-prefix"
	if _, err := restarted.svc.SaveCloudStorage(ctx, reuse); err != nil {
		t.Fatal(err)
	}
	if err := restarted.svc.DisconnectCloudStorage(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.svc.snapshotCfg.Enabled = true // Disconnect must override environment fallback too.
	restarted.svc.snapshotCfg.Bucket = "environment-backups"
	if err := restarted.svc.loadCloudStorage(ctx); err != nil {
		t.Fatal(err)
	}
	status, _ = restarted.svc.GetCloudStorage(ctx)
	if status.Connected || status.HasSavedCredentials {
		t.Fatal("disconnect did not survive restart")
	}
	record, exists, err := restarted.svc.cloudSettings.Load()
	if err != nil || !exists || !record.Disconnected || record.Connection.SecretAccessKey != "" || record.Connection.AccessKeyID != "" || record.Connection.SessionToken != "" {
		t.Fatal("disconnect retained credentials")
	}
}

func TestCloudStorageFailurePreservesConnectionAndRejectsBusyChanges(t *testing.T) {
	t.Parallel()
	server, state := newConnectionFixture(t)
	f := cloudFixture(t)
	req := cloudRequest(server.URL)
	ctx := context.Background()
	if _, err := f.svc.SaveCloudStorage(ctx, req); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(f.svc.snapshotCfg.SettingsDir, "connection.enc"))
	state.mu.Lock()
	state.denyList = true
	state.mu.Unlock()
	_, err := f.svc.SaveCloudStorage(ctx, req)
	if httpStatus(err) != http.StatusBadGateway || strings.Contains(err.Error(), req.SecretAccessKey) {
		t.Fatalf("unsafe connection error: %v", err)
	}
	if !strings.Contains(err.Error(), "s3:ListBucket") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("missing list failure diagnosis: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(f.svc.snapshotCfg.SettingsDir, "connection.enc"))
	if !bytes.Equal(original, after) {
		t.Fatal("failed connection test overwrote saved settings")
	}
	release, err := f.svc.reserveSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SaveCloudStorage(ctx, req); httpStatus(err) != http.StatusConflict {
		t.Errorf("changed destination during backup: %v", err)
	}
	if err := f.svc.DisconnectCloudStorage(ctx); httpStatus(err) != http.StatusConflict {
		t.Errorf("disconnected during backup: %v", err)
	}
	release()
	state.mu.Lock()
	state.denyList = false
	state.denyDelete = true
	state.mu.Unlock()
	if err := f.svc.TestCloudStorage(ctx, req); httpStatus(err) != http.StatusBadGateway || !strings.Contains(err.Error(), "s3:DeleteObject") {
		t.Fatalf("missing delete permission accepted: %v", err)
	}
}

func TestCloudStorageServerIdentityAndCredentialValidation(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "server-role-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "server-role-secret") // pragma: allowlist secret -- fake role for local HTTP fixture.
	t.Setenv("AWS_SESSION_TOKEN", "")
	server, state := newConnectionFixture(t)
	f := cloudFixture(t)
	req := cloudRequest(server.URL)
	req.AuthMode = snapshot.AuthServerIdentity
	req.AccessKeyID, req.SecretAccessKey, req.SessionToken = "", "", ""
	if _, err := f.svc.SaveCloudStorage(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	for _, header := range state.credentials {
		if !strings.Contains(header, "Credential=server-role-key/") {
			t.Error("server identity not used")
		}
	}
	state.mu.Unlock()
	req.SecretAccessKey = "must-not-be-accepted" // pragma: allowlist secret -- rejected test credential.
	if err := f.svc.TestCloudStorage(context.Background(), req); httpStatus(err) != http.StatusBadRequest {
		t.Fatalf("server identity accepted explicit secret: %v", err)
	}
}
