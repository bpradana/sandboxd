//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fasthttp/websocket"

	"github.com/bpradana/sandboxd/internal/presentation"
)

// terminal is a client of the terminal WebSocket that accumulates output.
type terminal struct {
	conn   *websocket.Conn
	output strings.Builder
	id     string
	final  *presentation.TerminalControlMessage
}

func (s *e2eSuite) terminalTicket(t *testing.T, id string) string {
	t.Helper()

	status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+id+"/terminal/tickets", nil)
	requireStatus(t, status, http.StatusCreated, envelope)
	return decode[presentation.TerminalTicketResponse](t, envelope).Ticket
}

// dialTerminal opens the terminal WebSocket with the given query and headers.
// It returns the handshake response so failures can be inspected.
func (s *e2eSuite) dialTerminal(t *testing.T, id string, query url.Values, header http.Header) (*terminal, *http.Response, error) {
	t.Helper()

	target := strings.Replace(s.baseURL, "http", "ws", 1) + "/api/v1/sandboxes/" + id + "/terminal?" + query.Encode()
	conn, res, err := websocket.DefaultDialer.Dial(target, header)
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		return nil, res, err
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &terminal{conn: conn}, res, nil
}

// openTerminal opens a terminal the way the web console does: ticket first,
// then the upgrade from the server's own origin.
func (s *e2eSuite) openTerminal(t *testing.T, id string, cols, rows int) *terminal {
	t.Helper()
	return s.attachTerminal(t, id, "", cols, rows)
}

// attachTerminal reattaches to terminalID, or opens a new terminal when it is
// empty, and waits for the "attached" message.
func (s *e2eSuite) attachTerminal(t *testing.T, id, terminalID string, cols, rows int) *terminal {
	t.Helper()

	query := url.Values{"ticket": {s.terminalTicket(t, id)}, "cols": {strconv.Itoa(cols)}, "rows": {strconv.Itoa(rows)}}
	if terminalID != "" {
		query.Set("terminal_id", terminalID)
	}
	header := http.Header{"Origin": {s.baseURL}}
	term, res, err := s.dialTerminal(t, id, query, header)
	if err != nil {
		status := 0
		if res != nil {
			status = res.StatusCode
		}
		t.Fatalf("open terminal: %v (status %d)", err, status)
	}
	deadline := time.Now().Add(10 * time.Second)
	for term.id == "" && term.final == nil {
		if _, err := term.read(deadline); err != nil {
			t.Fatalf("waiting for the attached message: %v", err)
		}
	}
	if term.final != nil {
		t.Fatalf("attach failed: %+v", term.final)
	}
	return term
}

func (term *terminal) closeTerminal(t *testing.T) {
	t.Helper()
	payload, _ := json.Marshal(presentation.TerminalControlMessage{Type: "close"})
	if err := term.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// attachFails reattaches to terminalID and returns the server's error.
func (s *e2eSuite) attachFails(t *testing.T, id, terminalID string) string {
	t.Helper()
	query := url.Values{"ticket": {s.terminalTicket(t, id)}, "terminal_id": {terminalID}}
	term, _, err := s.dialTerminal(t, id, query, http.Header{"Origin": {s.baseURL}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	final := term.waitClosed(t, 10*time.Second)
	if final == nil || final.Type != "error" {
		t.Fatalf("reattach to %s: final message %+v, terminal id %q", terminalID, final, term.id)
	}
	return final.Error
}

func (term *terminal) send(t *testing.T, input string) {
	t.Helper()
	if err := term.conn.WriteMessage(websocket.BinaryMessage, []byte(input)); err != nil {
		t.Fatalf("send %q: %v", input, err)
	}
}

func (term *terminal) resize(t *testing.T, cols, rows uint16) {
	t.Helper()
	payload, _ := json.Marshal(presentation.TerminalControlMessage{Type: "resize", Cols: cols, Rows: rows})
	if err := term.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("resize: %v", err)
	}
}

// read consumes one message; it reports false when the socket has closed.
func (term *terminal) read(deadline time.Time) (bool, error) {
	_ = term.conn.SetReadDeadline(deadline)
	messageType, data, err := term.conn.ReadMessage()
	if err != nil {
		return false, err
	}
	if messageType == websocket.TextMessage {
		var msg presentation.TerminalControlMessage
		if json.Unmarshal(data, &msg) == nil {
			if msg.Type == "attached" {
				term.id = msg.TerminalID
			} else {
				term.final = &msg
			}
		}
		return true, nil
	}
	term.output.Write(data)
	return true, nil
}

// expect reads until the output contains want.
func (term *terminal) expect(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(term.output.String(), want) {
		if _, err := term.read(deadline); err != nil {
			t.Fatalf("waiting for %q: %v; output so far:\n%s", want, err, term.output.String())
		}
	}
}

// waitClosed reads until the server closes the socket and returns the final
// control message, if one was sent.
func (term *terminal) waitClosed(t *testing.T, timeout time.Duration) *presentation.TerminalControlMessage {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := term.read(deadline); err != nil {
			var netErr interface{ Timeout() bool }
			if errors.As(err, &netErr) && netErr.Timeout() {
				t.Fatalf("terminal still open after %s; output:\n%s", timeout, term.output.String())
			}
			return term.final
		}
	}
}

func (s *e2eSuite) runTerminalCase(t *testing.T) {
	t.Run("interactive shell on a pty", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		term := s.openTerminal(t, sandbox.ID, 100, 30)

		// Markers are built by the shell, so the echoed command never matches.
		term.send(t, `echo "T:$(tty):$(id -u):$(stty size):$TERM"`+"\r")
		term.expect(t, "T:/dev/pts/0:0:30 100:xterm-256color", 10*time.Second)

		term.resize(t, 120, 40)
		term.send(t, `echo "S:$(stty size)"`+"\r")
		term.expect(t, "S:40 120", 10*time.Second)

		term.send(t, "sleep 100\r")
		time.Sleep(500 * time.Millisecond)
		term.send(t, "\x03")
		term.send(t, "echo C$((1+2))\r")
		term.expect(t, "C3", 10*time.Second)

		term.send(t, "exit 3\r")
		final := term.waitClosed(t, 10*time.Second)
		if final == nil || final.Type != "exit" || final.ExitCode == nil || *final.ExitCode != 3 {
			t.Fatalf("final message = %+v", final)
		}
	})

	t.Run("commands start with default signal handling", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		// Run grep directly: busybox sh -c itself ignores SIGQUIT for its
		// children.
		status, envelope := s.call(t, http.MethodPost, "/api/v1/sandboxes/"+sandbox.ID+"/exec", presentation.ExecRequest{
			Argv: []string{"grep", "-E", "^Sig(Ign|Blk)", "/proc/self/status"},
		})
		requireStatus(t, status, http.StatusOK, envelope)
		got := decode[presentation.ExecResponse](t, envelope).Stdout
		if strings.Count(got, "0000000000000000") != 2 {
			t.Fatalf("commands inherit ignored or blocked signals: %q", got)
		}
		result := s.exec(t, sandbox.ID, "sleep 30 & pid=$!; kill $pid; wait $pid; echo $?", 10)
		if result.Stdout != "143\n" {
			t.Fatalf("SIGTERM did not end a background job: %+v", result)
		}
	})

	t.Run("a terminal survives its client and can be reattached", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		first := s.openTerminal(t, sandbox.ID, 80, 24)
		first.send(t, "X=kept; echo M$((5+5))\r")
		first.expect(t, "M10", 10*time.Second)
		// Drop the socket without "close", like a reloaded page.
		_ = first.conn.Close()

		second := s.attachTerminal(t, sandbox.ID, first.id, 100, 30)
		if second.id != first.id {
			t.Fatalf("reattached to %q, want %q", second.id, first.id)
		}
		second.expect(t, "M10", 10*time.Second) // replayed scrollback
		second.send(t, `echo "V:$X:$(stty size)"`+"\r")
		second.expect(t, "V:kept:30 100", 10*time.Second)

		// A newer attachment takes over; the older client is told why.
		third := s.attachTerminal(t, sandbox.ID, first.id, 80, 24)
		if final := second.waitClosed(t, 10*time.Second); final == nil || !strings.Contains(final.Error, "another window") {
			t.Fatalf("taken-over client got %+v", final)
		}

		// "close" ends the terminal; it cannot be reattached.
		third.closeTerminal(t)
		if final := third.waitClosed(t, 10*time.Second); final == nil || final.Type != "exit" {
			t.Fatalf("final message after close = %+v", final)
		}
		if msg := s.attachFails(t, sandbox.ID, first.id); !strings.Contains(msg, "terminal not found") {
			t.Fatalf("reattach after close: %q", msg)
		}
	})

	t.Run("a detached terminal ends after the detach window", func(t *testing.T) {
		window, err := time.ParseDuration(os.Getenv(envDetachTimeout))
		if err != nil {
			t.Skipf("%s is not set to the server's SANDBOX_TERMINAL_DETACH_TIMEOUT", envDetachTimeout)
		}
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		term := s.openTerminal(t, sandbox.ID, 80, 24)
		_ = term.conn.Close()
		time.Sleep(window + 3*time.Second)
		if msg := s.attachFails(t, sandbox.ID, term.id); !strings.Contains(msg, "terminal not found") {
			t.Fatalf("reattach after the detach window: %q", msg)
		}
	})

	t.Run("tickets are single use and origins are checked", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		ticket := s.terminalTicket(t, sandbox.ID)
		sameOrigin := http.Header{"Origin": {s.baseURL}}

		if _, res, err := s.dialTerminal(t, sandbox.ID, url.Values{"ticket": {ticket}}, http.Header{"Origin": {"https://evil.example"}}); err == nil || res == nil || res.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign origin: err %v, response %+v", err, res)
		}
		if _, _, err := s.dialTerminal(t, sandbox.ID, url.Values{"ticket": {ticket}}, sameOrigin); err != nil {
			t.Fatalf("first use: %v", err)
		}
		if _, res, err := s.dialTerminal(t, sandbox.ID, url.Values{"ticket": {ticket}}, sameOrigin); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("reused ticket: err %v, response %+v", err, res)
		}
		if s.apiKey != "" {
			if _, res, err := s.dialTerminal(t, sandbox.ID, url.Values{}, sameOrigin); err == nil || res == nil || res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("no credentials: err %v, response %+v", err, res)
			}
			if _, _, err := s.dialTerminal(t, sandbox.ID, url.Values{}, http.Header{"Authorization": {"Bearer " + s.apiKey}}); err != nil {
				t.Fatalf("bearer key: %v", err)
			}
		}
	})

	t.Run("an idle terminal survives server timeouts", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		term := s.openTerminal(t, sandbox.ID, 80, 24)
		// Longer than HTTP_READ_TIMEOUT (30s); pings keep the socket alive
		// and the attached terminal keeps the sandbox from going idle.
		time.Sleep(40 * time.Second)
		term.send(t, "echo A$((2+2))\r")
		term.expect(t, "A4", 10*time.Second)
	})

	t.Run("an attached terminal keeps the sandbox from going idle", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{IdleTimeoutSeconds: 3})
		term := s.openTerminal(t, sandbox.ID, 80, 24)
		time.Sleep(8 * time.Second)
		if got := s.getSandbox(t, sandbox.ID); got.State != "running" {
			t.Fatalf("sandbox %s with a terminal attached (reason %q)", got.State, got.StopReason)
		}
		_ = term.conn.Close()
		s.waitStopped(t, sandbox.ID, "idle", 30*time.Second)
	})

	t.Run("polling does not keep an idle sandbox alive", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{IdleTimeoutSeconds: 3})
		deadline := time.Now().Add(30 * time.Second)
		for {
			got := s.getSandbox(t, sandbox.ID)
			if got.State == "stopped" {
				if got.StopReason != "idle" {
					t.Fatalf("stop reason = %q", got.StopReason)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("sandbox polled like the web console never went idle")
			}
			s.call(t, http.MethodGet, "/api/v1/sandboxes/"+sandbox.ID+"/stats", nil)
			time.Sleep(time.Second)
		}
	})

	t.Run("deleting the sandbox ends its terminals", func(t *testing.T) {
		sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
		term := s.openTerminal(t, sandbox.ID, 80, 24)
		term.send(t, "echo R$((3+4))\r")
		term.expect(t, "R7", 10*time.Second)

		status, envelope := s.call(t, http.MethodDelete, "/api/v1/sandboxes/"+sandbox.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		final := term.waitClosed(t, 20*time.Second)
		if final == nil || final.Type != "error" || !strings.Contains(final.Error, "not running") {
			t.Fatalf("final message = %+v", final)
		}
	})
}

func (s *e2eSuite) waitStopped(t *testing.T, id, reason string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := s.getSandbox(t, id)
		if got.State == "stopped" {
			if got.StopReason != reason {
				t.Fatalf("stop reason = %q, want %q", got.StopReason, reason)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sandbox still %s after %s", got.State, timeout)
		}
		time.Sleep(time.Second)
	}
}
