//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

const (
	envBaseURL  = "SANDBOXD_E2E_URL"
	envAPIKey   = "SANDBOXD_E2E_API_KEY" // pragma: allowlist secret -- an env var name.
	envInternet = "SANDBOXD_E2E_INTERNET"
	// envDetachTimeout is the server's SANDBOX_TERMINAL_DETACH_TIMEOUT;
	// the detach-window case runs only when it is set.
	envDetachTimeout = "SANDBOXD_E2E_DETACH_TIMEOUT"
)

type e2eSuite struct {
	baseURL  string
	apiKey   string
	internet bool
	client   *http.Client
}

type apiEnvelope struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"error_code"`
}

func newE2ESuite(t *testing.T) *e2eSuite {
	t.Helper()

	baseURL := strings.TrimRight(os.Getenv(envBaseURL), "/")
	if baseURL == "" {
		t.Skipf("%s is not set", envBaseURL)
	}
	suite := &e2eSuite{
		baseURL:  baseURL,
		apiKey:   os.Getenv(envAPIKey),
		internet: os.Getenv(envInternet) == "1",
		client:   &http.Client{Timeout: 2 * time.Minute},
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		res, err := suite.client.Get(baseURL + "/readyz")
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return suite
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandboxd at %s never became ready (last error %v)", baseURL, err)
		}
		time.Sleep(time.Second)
	}
}

// call sends a JSON request and returns the status and decoded envelope.
func (s *e2eSuite) call(t *testing.T, method, path string, body any) (int, apiEnvelope) {
	t.Helper()

	status, raw := s.raw(t, method, path, body, s.apiKey)
	var envelope apiEnvelope
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	return status, envelope
}

func (s *e2eSuite) raw(t *testing.T, method, path string, body any, apiKey string) (int, []byte) {
	t.Helper()

	var reader io.Reader
	contentType := "application/json"
	switch value := body.(type) {
	case nil:
	case []byte:
		reader = bytes.NewReader(value)
		contentType = "application/octet-stream"
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if reader != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

func decode[T any](t *testing.T, envelope apiEnvelope) T {
	t.Helper()

	var value T
	if err := json.Unmarshal(envelope.Data, &value); err != nil {
		t.Fatalf("decode data %s: %v", envelope.Data, err)
	}
	return value
}

// createSandbox creates a sandbox that is destroyed when the test ends.
func (s *e2eSuite) createSandbox(t *testing.T, req presentation.CreateSandboxRequest) presentation.SandboxResponse {
	t.Helper()

	if req.Labels == nil {
		req.Labels = map[string]string{"suite": "e2e"}
	}
	status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes", req)
	if status != http.StatusCreated {
		t.Fatalf("create sandbox: status %d, %+v", status, envelope)
	}
	sandbox := decode[presentation.SandboxResponse](t, envelope)
	t.Cleanup(func() {
		s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+sandbox.ID, nil)
	})
	return sandbox
}

func (s *e2eSuite) exec(t *testing.T, id, command string, timeoutSeconds int) presentation.ExecResponse {
	t.Helper()

	status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+id+"/exec", presentation.ExecRequest{
		Command:        command,
		TimeoutSeconds: timeoutSeconds,
	})
	if status != http.StatusOK {
		t.Fatalf("exec %q: status %d, %+v", command, status, envelope)
	}
	return decode[presentation.ExecResponse](t, envelope)
}

// execAs runs command as user ("root" or "sandbox").
func (s *e2eSuite) execAs(t *testing.T, id, user, command string, timeoutSeconds int) presentation.ExecResponse {
	t.Helper()

	status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+id+"/exec", presentation.ExecRequest{
		Command:        command,
		User:           user,
		TimeoutSeconds: timeoutSeconds,
	})
	if status != http.StatusOK {
		t.Fatalf("exec %q as %s: status %d, %+v", command, user, status, envelope)
	}
	return decode[presentation.ExecResponse](t, envelope)
}

// mustSucceed runs command and fails the test unless it exits 0.
func (s *e2eSuite) mustSucceed(t *testing.T, id, command string) string {
	t.Helper()

	result := s.exec(t, id, command, 30)
	if result.ExitCode != 0 {
		t.Fatalf("%q exited %d: stdout=%q stderr=%q", command, result.ExitCode, result.Stdout, result.Stderr)
	}
	return result.Stdout
}

func (s *e2eSuite) stats(t *testing.T, id string) presentation.SandboxStatsResponse {
	t.Helper()

	status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/"+id+"/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("stats: status %d, %+v", status, envelope)
	}
	return decode[presentation.SandboxStatsResponse](t, envelope)
}

func (s *e2eSuite) getSandbox(t *testing.T, id string) presentation.SandboxResponse {
	t.Helper()

	status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/"+id, nil)
	if status != http.StatusOK {
		t.Fatalf("get sandbox: status %d, %+v", status, envelope)
	}
	return decode[presentation.SandboxResponse](t, envelope)
}

func requireStatus(t *testing.T, got, want int, envelope apiEnvelope) {
	t.Helper()
	if got != want {
		t.Fatalf("status %d, want %d: %s", got, want, fmt.Sprintf("%+v", envelope))
	}
}
