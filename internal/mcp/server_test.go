package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bpradana/sandboxd/internal/client"
	"github.com/bpradana/sandboxd/internal/helper/buildinfo"
)

// fakeDaemon answers the handful of endpoints the tools use, so these tests
// exercise the tools and their wording rather than the daemon.
type fakeDaemon struct {
	mux      *http.ServeMux
	requests []string
}

func newFakeDaemon(t *testing.T) (*httptest.Server, *fakeDaemon) {
	t.Helper()
	daemon := &fakeDaemon{mux: http.NewServeMux()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		daemon.requests = append(daemon.requests, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(body))
		daemon.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, daemon
}

func (d *fakeDaemon) handle(pattern string, status int, data any) {
	d.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		payload, _ := json.Marshal(map[string]any{"code": status, "data": data})
		_, _ = w.Write(payload)
	})
}

func (d *fakeDaemon) refuse(pattern string, status int, code, message string) {
	d.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		payload, _ := json.Marshal(map[string]any{"code": status, "error_code": code, "message": message})
		_, _ = w.Write(payload)
	})
}

// lastCommand decodes the shell command from the most recent request, so
// assertions read the command the sandbox would run rather than its JSON
// escaping.
func (d *fakeDaemon) lastCommand(t *testing.T) string {
	t.Helper()
	if len(d.requests) == 0 {
		t.Fatal("the daemon was never called")
	}
	last := d.requests[len(d.requests)-1]
	_, body, ok := strings.Cut(last, " {")
	if !ok {
		t.Fatalf("no body in %q", last)
	}
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte("{"+body), &decoded); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return decoded.Command
}

// session connects an MCP client to the server under test, in memory.
func session(t *testing.T, daemonURL string) *mcp.ClientSession {
	t.Helper()
	api, err := client.New(daemonURL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server := NewServer(api, buildinfo.Info{Version: "test"})
	ctx := t.Context()
	go func() { _ = server.Run(ctx, serverTransport) }()

	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// call runs a tool and returns its text, plus whether it was reported as a
// failure. A refused operation must come back as a failed tool call, never as
// a protocol error, so the model can read it and try something else.
func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := sess.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	var out strings.Builder
	for _, content := range result.Content {
		if textContent, ok := content.(*mcp.TextContent); ok {
			out.WriteString(textContent.Text)
		}
	}
	return out.String(), result.IsError
}

func TestToolsAreDiscoverableWithSchemas(t *testing.T) {
	server, _ := newFakeDaemon(t)
	sess := session(t, server.URL)

	tools, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]*mcp.Tool{}
	for _, tool := range tools.Tools {
		found[tool.Name] = tool
	}
	for _, name := range []string{
		"sandbox_create", "sandbox_list", "sandbox_exec", "sandbox_keepalive", "sandbox_destroy",
		"sandbox_write_file", "sandbox_read_file", "sandbox_list_files",
		"sandbox_ports", "sandbox_preview", "sandbox_changes",
	} {
		tool, ok := found[name]
		if !ok {
			t.Fatalf("%s is not offered", name)
		}
		if tool.Description == "" || tool.InputSchema == nil {
			t.Errorf("%s has no description or input schema", name)
		}
	}
	// Destroying a sandbox is not something to retry blindly.
	if destructive := found["sandbox_destroy"].Annotations.DestructiveHint; destructive == nil || !*destructive {
		t.Error("sandbox_destroy is not marked destructive")
	}
	if !found["sandbox_list"].Annotations.ReadOnlyHint {
		t.Error("sandbox_list is not marked read-only")
	}
}

func TestCreateDefaultsToABoundedLifetime(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("POST /api/v1/sandboxes", http.StatusCreated, map[string]any{
		"id": "sbx-1", "image": "alpine", "network": "none", "state": "running",
		"expires_at": "2026-10-30T12:00:00Z",
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_create", map[string]any{"network": "none"})
	if failed {
		t.Fatalf("create failed: %s", out)
	}
	if !strings.Contains(out, "sbx-1") {
		t.Errorf("the model is not told the sandbox ID: %s", out)
	}
	// An agent that forgets to clean up should not leave a sandbox running
	// indefinitely, so an unspecified lifetime is bounded here.
	if !strings.Contains(daemon.requests[0], `"ttl_seconds":1800`) {
		t.Errorf("no default lifetime was applied: %s", daemon.requests[0])
	}
}

func TestExecReportsExitCodeAndBothStreams(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("POST /api/v1/sandboxes/sbx-1/exec", http.StatusOK, map[string]any{
		"exit_code": 2, "stdout": "out here", "stderr": "err here", "stderr_truncated": true,
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_exec", map[string]any{"sandbox_id": "sbx-1", "command": "false"})
	if failed {
		t.Fatalf("exec reported a tool failure for a non-zero exit: %s", out)
	}
	for _, want := range []string{"exit code 2", "out here", "err here", "truncated"} {
		if !strings.Contains(out, want) {
			t.Errorf("exec result is missing %q: %s", want, out)
		}
	}
}

func TestRefusalsComeBackAsReadableToolFailures(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.refuse("POST /api/v1/sandboxes/sbx-gone/exec", http.StatusConflict, "sandbox_not_running", "sandbox is not running")
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_exec", map[string]any{"sandbox_id": "sbx-gone", "command": "true"})
	if !failed {
		t.Fatal("a refused operation was reported as success")
	}
	// The daemon's stable code is worth passing through: it tells a model
	// what to do next far better than a status number.
	if !strings.Contains(out, "sandbox is not running") || !strings.Contains(out, "sandbox_not_running") {
		t.Errorf("the refusal lost its reason: %s", out)
	}
}

func TestPortsGuidesTheModelWhenNothingIsListening(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/ports", http.StatusOK, []any{})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_ports", map[string]any{"sandbox_id": "sbx-1"})
	if failed {
		t.Fatalf("ports failed: %s", out)
	}
	if !strings.Contains(out, "Nothing is listening") || !strings.Contains(out, "background") {
		t.Errorf("an empty port list does not say what to do next: %s", out)
	}
}

func TestPreviewWarnsWhenTheURLWouldNotLoad(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("POST /api/v1/sandboxes/sbx-1/previews", http.StatusCreated, map[string]any{
		"id": "pv1", "url": "http://abc.preview.test/", "port": 3000, "expires_at": "2026-10-30T12:00:00Z",
	})
	daemon.handle("GET /api/v1/sandboxes/sbx-1/ports", http.StatusOK, []any{
		map[string]any{"port": 3000, "address": "192.168.1.2", "previewable": false},
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_preview", map[string]any{"sandbox_id": "sbx-1", "port": 3000})
	if failed {
		t.Fatalf("preview failed: %s", out)
	}
	if !strings.Contains(out, "http://abc.preview.test/") {
		t.Errorf("the URL to hand to a person is missing: %s", out)
	}
	// Handing a person a link that cannot load is worse than saying why.
	if !strings.Contains(out, "0.0.0.0") {
		t.Errorf("an unreachable binding was not called out: %s", out)
	}
}

func TestReadFileRefusesWhatAModelCannotUse(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.mux.HandleFunc("GET /api/v1/sandboxes/sbx-1/files", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte{0xff, 0xfe, 0x00, 0x01})
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_read_file", map[string]any{"sandbox_id": "sbx-1", "path": "/root/blob"})
	if failed {
		t.Fatalf("read failed: %s", out)
	}
	if !strings.Contains(out, "not valid UTF-8") || !strings.Contains(out, "sandbox_exec") {
		t.Errorf("binary content was not handled usefully: %s", out)
	}
}

// One-shot exec reaps its process group when the command returns, so an agent
// that backgrounds a server there silently loses it. start_service uses a
// session, whose shell outlives the call.
func TestStartServiceUsesASessionSoTheProcessSurvives(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/sessions", http.StatusOK, []any{})
	daemon.handle("POST /api/v1/sandboxes/sbx-1/sessions", http.StatusCreated, map[string]any{"id": "ses-1", "state": "idle"})
	daemon.handle("POST /api/v1/sandboxes/sbx-1/sessions/ses-1/exec", http.StatusOK, map[string]any{
		"exit_code": 0, "stdout": "started pid 42\n",
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_start_service", map[string]any{
		"sandbox_id": "sbx-1", "command": "python3 -m http.server 8000",
	})
	if failed {
		t.Fatalf("start_service failed: %s", out)
	}
	if !strings.Contains(daemon.requests[len(daemon.requests)-1], "/sessions/ses-1/exec") {
		t.Fatalf("the process was not started in a session: %s", daemon.requests[len(daemon.requests)-1])
	}
	// Backgrounded, with its output kept somewhere the agent can read it.
	launched := daemon.lastCommand(t)
	if !strings.HasSuffix(strings.SplitN(launched, "\n", 2)[0], "&") {
		t.Errorf("the command was not backgrounded: %s", launched)
	}
	if !strings.Contains(launched, "python3 -m http.server 8000") {
		t.Errorf("the command was mangled: %s", launched)
	}
	if !strings.Contains(out, "/tmp/python3.log") || !strings.Contains(out, "sandbox_ports") {
		t.Errorf("the result does not say where the output went or what to do next: %s", out)
	}
}

func TestStartServiceReusesTheSandboxesExistingShell(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/sessions", http.StatusOK, []any{
		map[string]any{"id": "ses-open", "state": "idle"},
	})
	daemon.mux.HandleFunc("POST /api/v1/sandboxes/sbx-1/sessions", func(http.ResponseWriter, *http.Request) {
		t.Error("a second shell was opened when one was already available")
	})
	daemon.handle("POST /api/v1/sandboxes/sbx-1/sessions/ses-open/exec", http.StatusOK, map[string]any{
		"exit_code": 0, "stdout": "started pid 7\n",
	})
	sess := session(t, server.URL)

	if out, failed := call(t, sess, "sandbox_start_service", map[string]any{"sandbox_id": "sbx-1", "command": "sleep 100"}); failed {
		t.Fatalf("start_service failed: %s", out)
	}
}

func TestStartServiceQuotesALogPathItDidNotChoose(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/sessions", http.StatusOK, []any{map[string]any{"id": "ses-1", "state": "idle"}})
	daemon.handle("POST /api/v1/sandboxes/sbx-1/sessions/ses-1/exec", http.StatusOK, map[string]any{"exit_code": 0, "stdout": "ok"})
	sess := session(t, server.URL)

	// A path a model supplied must not be able to end the redirect and run
	// something else.
	if _, failed := call(t, sess, "sandbox_start_service", map[string]any{
		"sandbox_id": "sbx-1", "command": "sleep 1", "log_path": "/tmp/a'; rm -rf /; echo '",
	}); failed {
		t.Fatal("start_service refused a quotable path")
	}
	// The injected text must survive only as part of one quoted filename,
	// never as a command the shell would run.
	launched := daemon.lastCommand(t)
	redirect := strings.SplitN(launched, ">", 2)[1]
	if !strings.HasPrefix(redirect, "'/tmp/a'") || !strings.Contains(redirect, `'\''`) {
		t.Errorf("a log path escaped its quoting: %s", launched)
	}
	if strings.Contains(launched, "; rm -rf /; echo ;") {
		t.Errorf("the injected text became shell syntax: %s", launched)
	}
}

func TestChangesHidesTheDaemonsOwnFilesUnlessAsked(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/changes", http.StatusOK, map[string]any{
		"entries": []map[string]any{
			{"path": "/etc/resolv.conf", "kind": "modified", "type": "file", "size": 20, "seeded": true},
			{"path": "/root/out", "kind": "added", "type": "dir", "seeded": false},
			{"path": "/root/report.md", "kind": "added", "type": "file", "size": 412, "seeded": false},
			{"path": "/root/old.txt", "kind": "deleted", "seeded": false},
		},
		"truncated": false,
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_changes", map[string]any{"sandbox_id": "sbx-1"})
	if failed {
		t.Fatalf("sandbox_changes failed: %s", out)
	}
	if !strings.Contains(out, "/root/report.md (412 bytes)") || !strings.Contains(out, "deleted   /root/old.txt") {
		t.Errorf("the workload's own changes are not reported plainly: %s", out)
	}
	if !strings.Contains(out, "/root/out/") {
		t.Errorf("a directory should read as one: %s", out)
	}
	if strings.Contains(out, "/etc/resolv.conf") {
		t.Errorf("files the daemon writes everywhere should be left out by default: %s", out)
	}
	if !strings.Contains(out, "include_seeded") {
		t.Errorf("leaving entries out without saying so hides them: %s", out)
	}

	out, _ = call(t, sess, "sandbox_changes", map[string]any{"sandbox_id": "sbx-1", "include_seeded": true})
	if !strings.Contains(out, "/etc/resolv.conf") {
		t.Errorf("include_seeded should show them: %s", out)
	}

	out, _ = call(t, sess, "sandbox_changes", map[string]any{"sandbox_id": "sbx-1", "only_deleted": true})
	if !strings.Contains(out, "/root/old.txt") || strings.Contains(out, "/root/report.md") {
		t.Errorf("only_deleted should keep deletions alone: %s", out)
	}
}

func TestChangesSaysSoWhenThereIsNothingToReport(t *testing.T) {
	server, daemon := newFakeDaemon(t)
	daemon.handle("GET /api/v1/sandboxes/sbx-1/changes", http.StatusOK, map[string]any{
		"entries": []map[string]any{}, "truncated": false,
	})
	sess := session(t, server.URL)

	out, failed := call(t, sess, "sandbox_changes", map[string]any{"sandbox_id": "sbx-1"})
	if failed || !strings.Contains(out, "Nothing in sbx-1") {
		t.Errorf("an untouched sandbox should say so: %s", out)
	}
}
