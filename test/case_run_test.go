//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
	"github.com/bpradana/sandboxd/internal/preview"
)

// openCommandTerminal opens a terminal that runs one command, the way a run
// tab in the web console does, and waits for the "attached" message.
func (s *e2eSuite) openCommandTerminal(t *testing.T, id, command string) *terminal {
	t.Helper()

	query := url.Values{"ticket": {s.terminalTicket(t, id)}, "cols": {"100"}, "rows": {"30"}, "command": {command}}
	term, res, err := s.dialTerminal(t, id, query, http.Header{"Origin": {s.baseURL}})
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("open command terminal: %v (status %d)", err, status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for term.id == "" && term.final == nil {
		if _, err := term.read(deadline); err != nil {
			t.Fatalf("waiting for the attached message: %v", err)
		}
	}
	if term.final != nil {
		t.Fatalf("running %q failed: %+v", command, term.final)
	}
	return term
}

func (s *e2eSuite) listeningPorts(t *testing.T, id string) []presentation.ListeningPortResponse {
	t.Helper()

	status, envelope := s.call(t, http.MethodGet, "/api/v1/sandboxes/"+id+"/ports", nil)
	requireStatus(t, status, http.StatusOK, envelope)
	return decode[[]presentation.ListeningPortResponse](t, envelope)
}

// waitForPort polls until the sandbox reports the port, or stops reporting it.
func (s *e2eSuite) waitForPort(t *testing.T, id string, port int, want bool) presentation.ListeningPortResponse {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	var last []presentation.ListeningPortResponse
	for time.Now().Before(deadline) {
		last = s.listeningPorts(t, id)
		for _, entry := range last {
			if want && entry.Port == port {
				return entry
			}
		}
		if !want && !containsPort(last, port) {
			return presentation.ListeningPortResponse{}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("port %d listening = %v after 20s; reported %+v", port, !want, last)
	return presentation.ListeningPortResponse{}
}

func containsPort(ports []presentation.ListeningPortResponse, port int) bool {
	for _, entry := range ports {
		if entry.Port == port {
			return true
		}
	}
	return false
}

func (s *e2eSuite) runRunCase(t *testing.T) {
	t.Run("a command runs on a pty and ends the terminal with its status", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		term := s.openCommandTerminal(t, sandbox.ID, `echo "R:$(tty):$TERM:$(stty size)"; exit 7`)

		// A run tab is a real terminal: the command owns a pty, is told the
		// window size, and its exit status ends the terminal.
		term.expect(t, "R:/dev/pts/0:xterm-256color:30 100", 10*time.Second)
		final := term.waitClosed(t, 10*time.Second)
		if final == nil || final.Type != "exit" || final.ExitCode == nil || *final.ExitCode != 7 {
			t.Fatalf("final message = %+v", final)
		}
	})

	t.Run("Ctrl-C and close both stop a command", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		interrupted := s.openCommandTerminal(t, sandbox.ID, `echo READY; sleep 60`)
		interrupted.expect(t, "READY", 10*time.Second)
		interrupted.send(t, "\x03")
		if final := interrupted.waitClosed(t, 10*time.Second); final == nil || final.Type != "exit" {
			t.Fatalf("Ctrl-C did not end the command: %+v", final)
		}

		// The console's Stop button sends "close" instead of keystrokes.
		stopped := s.openCommandTerminal(t, sandbox.ID, `echo READY; sleep 60`)
		stopped.expect(t, "READY", 10*time.Second)
		stopped.closeTerminal(t)
		if final := stopped.waitClosed(t, 10*time.Second); final == nil || final.Type != "exit" {
			t.Fatalf("close did not end the command: %+v", final)
		}
	})

	t.Run("listening ports are reported from the sandbox and can be previewed", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		// The host is full of listening sockets, the API's own among them. A
		// fresh sandbox has none, which is how this proves the tables come
		// from the sandbox's network namespace.
		if ports := s.listeningPorts(t, sandbox.ID); len(ports) != 0 {
			t.Fatalf("a fresh sandbox reports listeners: %+v", ports)
		}

		server := s.openCommandTerminal(t, sandbox.ID, "nc -l -p 3000 -e echo reachable")
		port := s.waitForPort(t, sandbox.ID, 3000, true)
		if !port.Previewable {
			t.Fatalf("a server on 0.0.0.0 is not previewable: %+v", port)
		}

		status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+sandbox.ID+"/previews", presentation.CreatePreviewRequest{Port: 3000})
		requireStatus(t, status, http.StatusCreated, envelope)
		if app := decode[preview.Session](t, envelope); app.Port != 3000 || app.URL == "" {
			t.Fatalf("preview on a reported port = %+v", app)
		}

		// Ending the command retires its socket, so the console stops
		// offering a port nothing listens on.
		server.closeTerminal(t)
		server.waitClosed(t, 10*time.Second)
		s.waitForPort(t, sandbox.ID, 3000, false)
	})

	t.Run("ports of a sandbox that is gone are a conflict, not an empty list", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		status, envelope := s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+sandbox.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		status, envelope = s.call(t, http.MethodGet, "/api/v1/sandboxes/"+sandbox.ID+"/ports", nil)
		if status != http.StatusNotFound && status != http.StatusConflict {
			t.Fatalf("ports of a destroyed sandbox = %d: %+v", status, envelope)
		}
	})
}
