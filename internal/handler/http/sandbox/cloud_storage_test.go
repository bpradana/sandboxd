package sandbox

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func TestCloudStorageEndpointsNeverReturnCredentials(t *testing.T) {
	t.Parallel()
	app, service := newApp(t)
	body := `{"auth_mode":"access_keys","bucket":"backups","region":"us-east-1","secret_access_key":"typed-secret","access_key_id":"typed-key"}`                        // pragma: allowlist secret -- handler fixture.
	req := presentation.CloudStorageRequest{AuthMode: "access_keys", Bucket: "backups", Region: "us-east-1", AccessKeyID: "typed-key", SecretAccessKey: "typed-secret"} // pragma: allowlist secret -- handler fixture.
	service.EXPECT().TestCloudStorage(gomock.Any(), req).Return(nil)
	res, _ := do(t, app, http.MethodPost, "/settings/cloud-storage/test", body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("test status %d", res.StatusCode)
	}
	service.EXPECT().SaveCloudStorage(gomock.Any(), req).Return(&presentation.CloudStorageResponse{Connected: true, Bucket: "backups", HasSavedCredentials: true}, nil)
	res, _ = do(t, app, http.MethodPut, "/settings/cloud-storage", body)
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("save status/cache %d %q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	data, _ := io.ReadAll(res.Body)
	if strings.Contains(string(data), "typed-secret") || strings.Contains(string(data), "typed-key") {
		t.Fatal("credentials returned")
	}
	service.EXPECT().GetCloudStorage(gomock.Any()).Return(&presentation.CloudStorageResponse{Connected: true}, nil)
	res, _ = do(t, app, http.MethodGet, "/settings/cloud-storage", "")
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("settings GET is not private")
	}
	service.EXPECT().DisconnectCloudStorage(gomock.Any()).Return(nil)
	res, _ = do(t, app, http.MethodDelete, "/settings/cloud-storage", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("disconnect status %d", res.StatusCode)
	}
}

func TestCloudStorageValidationDoesNotEchoSecret(t *testing.T) {
	t.Parallel()
	app, _ := newApp(t)
	secret := strings.Repeat("sensitive-", 500)
	body, _ := json.Marshal(presentation.CloudStorageRequest{AuthMode: "access_keys", Bucket: "backups", Region: "us-east-1", AccessKeyID: "key", SecretAccessKey: secret})
	res, _ := do(t, app, http.MethodPut, "/settings/cloud-storage", string(body))
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusBadRequest || strings.Contains(string(data), "sensitive-") {
		t.Fatalf("unsafe validation response: status %d", res.StatusCode)
	}
	request := httptest.NewRequest(http.MethodPost, "/settings/cloud-storage/test", strings.NewReader(`{"auth_mode":"server_identity","bucket":"backups","region":"us-east-1"}`))
	request.Header.Set("Content-Type", "text/plain")
	res, err := app.Test(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("cross-site simple POST accepted: %d", res.StatusCode)
	}
}
