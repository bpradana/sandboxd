// Package client talks to a sandboxd daemon over its REST API. It exists for
// processes that are not the daemon -- the MCP server, operator tooling --
// and so deliberately shares no state with the service layer: everything it
// can do, any API client can do.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// DefaultBaseURL is where a daemon listens with the shipped configuration.
const DefaultBaseURL = "http://127.0.0.1:8080"

// maxErrorBody bounds what is read from a failed response before giving up on
// finding an error message in it.
const maxErrorBody = 64 << 10

// Error is a refused request. Code is the daemon's stable error code, which
// identifies the condition far better than the status alone.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("sandboxd: %s (%d)", e.Message, e.Status)
	}
	return fmt.Sprintf("sandboxd: %s (%s)", e.Message, e.Code)
}

type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

type Option func(*Client)

// WithHTTPClient replaces the transport, which tests use and callers needing
// a longer timeout than the default do too.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.http = h }
}

// New returns a client for a daemon. The key is sent as a bearer token and
// may be empty when the daemon runs unauthenticated.
func New(baseURL, apiKey string, options ...Option) (*Client, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("sandboxd url must be an http(s) URL, got %q", baseURL)
	}
	// A restore downloads and extracts a whole archive before answering, so
	// the default allows for a slow bucket rather than a quick failure.
	c := &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Minute}}
	for _, option := range options {
		option(c)
	}
	return c, nil
}

// envelope is the daemon's response shape. Errors carry a code and either a
// message or a field map, depending on where they were raised.
type envelope struct {
	Message   string              `json:"message"`
	Error     string              `json:"error"`
	ErrorCode string              `json:"error_code"`
	Errors    map[string][]string `json:"errors"`
	Data      json.RawMessage     `json:"data"`
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandboxd at %s is unreachable: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()

	payload, readErr := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	var decoded envelope
	_ = json.Unmarshal(payload, &decoded)
	if res.StatusCode >= 300 {
		return nil, &Error{Status: res.StatusCode, Code: decoded.ErrorCode, Message: errorMessage(decoded, res.Status)}
	}
	if readErr != nil {
		return nil, readErr
	}
	return decoded.Data, nil
}

// errorMessage prefers the most specific wording the daemon offered, since a
// bare status tells an agent nothing about how to proceed.
func errorMessage(e envelope, status string) string {
	if len(e.Errors) > 0 {
		fields := make([]string, 0, len(e.Errors))
		for field, problems := range e.Errors {
			fields = append(fields, field+": "+strings.Join(problems, ", "))
		}
		return strings.Join(fields, "; ")
	}
	for _, candidate := range []string{e.Error, e.Message} {
		if candidate != "" {
			return candidate
		}
	}
	return status
}

func decode[T any](data json.RawMessage, err error) (T, error) {
	var value T
	if err != nil {
		return value, err
	}
	if len(data) == 0 {
		return value, nil
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("decode sandboxd response: %w", err)
	}
	return value, nil
}

// IsNotFound reports a sandbox, snapshot or path the daemon does not have.
func IsNotFound(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// ---------- sandboxes ----------

func (c *Client) CreateSandbox(ctx context.Context, req presentation.CreateSandboxRequest) (presentation.SandboxResponse, error) {
	return decode[presentation.SandboxResponse](c.do(ctx, http.MethodPost, "/api/v1/sandboxes", nil, req))
}

func (c *Client) ListSandboxes(ctx context.Context) ([]presentation.SandboxResponse, error) {
	return decode[[]presentation.SandboxResponse](c.do(ctx, http.MethodGet, "/api/v1/sandboxes", nil, nil))
}

func (c *Client) GetSandbox(ctx context.Context, id string) (presentation.SandboxResponse, error) {
	return decode[presentation.SandboxResponse](c.do(ctx, http.MethodGet, sandboxPath(id), nil, nil))
}

func (c *Client) DeleteSandbox(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, sandboxPath(id), nil, nil)
	return err
}

func (c *Client) Keepalive(ctx context.Context, id string, extendSeconds int) (presentation.SandboxResponse, error) {
	return decode[presentation.SandboxResponse](c.do(ctx, http.MethodPost, sandboxPath(id)+"/keepalive", nil,
		presentation.KeepaliveRequest{TTLSeconds: extendSeconds}))
}

func (c *Client) Exec(ctx context.Context, id string, req presentation.ExecRequest) (presentation.ExecResponse, error) {
	return decode[presentation.ExecResponse](c.do(ctx, http.MethodPost, sandboxPath(id)+"/exec", nil, req))
}

func (c *Client) ListeningPorts(ctx context.Context, id string) ([]presentation.ListeningPortResponse, error) {
	return decode[[]presentation.ListeningPortResponse](c.do(ctx, http.MethodGet, sandboxPath(id)+"/ports", nil, nil))
}

func (c *Client) Changes(ctx context.Context, id string) (presentation.SandboxChangesResponse, error) {
	return decode[presentation.SandboxChangesResponse](c.do(ctx, http.MethodGet, sandboxPath(id)+"/changes", nil, nil))
}

// PromoteSnapshot imports a stored snapshot as a base image. It can take as
// long as a restore does, because it is the same download and extraction.
func (c *Client) PromoteSnapshot(ctx context.Context, snapshotID, name string) (presentation.ImageResponse, error) {
	path := "/api/v1/snapshots/" + url.PathEscape(snapshotID) + "/image"
	return decode[presentation.ImageResponse](c.do(ctx, http.MethodPost, path, nil,
		presentation.PromoteSnapshotRequest{Image: name}))
}

// PreviewSession mirrors what the preview endpoint returns. It is declared
// here rather than imported, because the preview package owns a live proxy
// and the engine behind it; a client needs neither.
type PreviewSession struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c *Client) CreatePreview(ctx context.Context, id string, port int) (PreviewSession, error) {
	return decode[PreviewSession](c.do(ctx, http.MethodPost, sandboxPath(id)+"/previews", nil,
		presentation.CreatePreviewRequest{Port: port}))
}

func (c *Client) RevokePreview(ctx context.Context, id, previewID string) error {
	_, err := c.do(ctx, http.MethodDelete, sandboxPath(id)+"/previews", nil, presentation.RevokePreviewRequest{ID: previewID})
	return err
}

// ---------- sessions ----------

// A session is a shell kept alive between calls, so its working directory,
// variables and background jobs survive. One-shot exec has none of that: its
// process group is reaped when the command returns.

func (c *Client) CreateSession(ctx context.Context, id string, req presentation.CreateSessionRequest) (presentation.SessionResponse, error) {
	return decode[presentation.SessionResponse](c.do(ctx, http.MethodPost, sandboxPath(id)+"/sessions", nil, req))
}

func (c *Client) ListSessions(ctx context.Context, id string) ([]presentation.SessionResponse, error) {
	return decode[[]presentation.SessionResponse](c.do(ctx, http.MethodGet, sandboxPath(id)+"/sessions", nil, nil))
}

func (c *Client) SessionExec(ctx context.Context, id, sessionID string, req presentation.SessionExecRequest) (presentation.ExecResponse, error) {
	return decode[presentation.ExecResponse](c.do(ctx, http.MethodPost,
		sandboxPath(id)+"/sessions/"+url.PathEscape(sessionID)+"/exec", nil, req))
}

// ---------- files ----------

func (c *Client) ListFiles(ctx context.Context, id, path string) ([]presentation.FileInfoResponse, error) {
	return decode[[]presentation.FileInfoResponse](c.do(ctx, http.MethodGet, sandboxPath(id)+"/files/list", url.Values{"path": {path}}, nil))
}

// ReadFile returns a file's bytes. Raw transfers do not use the JSON envelope.
func (c *Client) ReadFile(ctx context.Context, id, path string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+sandboxPath(id)+"/files?"+url.Values{"path": {path}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandboxd at %s is unreachable: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
		var decoded envelope
		_ = json.Unmarshal(payload, &decoded)
		return nil, &Error{Status: res.StatusCode, Code: decoded.ErrorCode, Message: errorMessage(decoded, res.Status)}
	}
	// One byte past the limit distinguishes "exactly at the limit" from
	// "truncated", so the caller can say which it was.
	content, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("file is larger than %d bytes; read it in the sandbox instead", maxBytes)
	}
	return content, nil
}

func (c *Client) WriteFile(ctx context.Context, id, path, mode string, content []byte) (presentation.FileInfoResponse, error) {
	query := url.Values{"path": {path}}
	if mode != "" {
		query.Set("mode", mode)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+sandboxPath(id)+"/files?"+query.Encode(), bytes.NewReader(content))
	if err != nil {
		return presentation.FileInfoResponse{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return presentation.FileInfoResponse{}, fmt.Errorf("sandboxd at %s is unreachable: %w", c.baseURL, err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	var decoded envelope
	_ = json.Unmarshal(payload, &decoded)
	if res.StatusCode >= 300 {
		return presentation.FileInfoResponse{}, &Error{Status: res.StatusCode, Code: decoded.ErrorCode, Message: errorMessage(decoded, res.Status)}
	}
	return decode[presentation.FileInfoResponse](decoded.Data, nil)
}

func sandboxPath(id string) string {
	return "/api/v1/sandboxes/" + url.PathEscape(id)
}
