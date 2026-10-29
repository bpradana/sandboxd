//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runSessionsCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})
	base := "/api/v1/sandboxes/" + sandbox.ID + "/sessions"

	status, envelope := s.call(t, http.MethodPost, base, presentation.CreateSessionRequest{Env: map[string]string{"START": "yes"}})
	requireStatus(t, status, http.StatusCreated, envelope)
	session := decode[presentation.SessionResponse](t, envelope)

	run := func(t *testing.T, command string, timeout int) (int, presentation.ExecResponse, apiEnvelope) {
		t.Helper()
		status, envelope := s.call(t, http.MethodPost, base+"/"+session.ID+"/exec", presentation.SessionExecRequest{Command: command, TimeoutSeconds: timeout})
		if status != http.StatusOK {
			return status, presentation.ExecResponse{}, envelope
		}
		return status, decode[presentation.ExecResponse](t, envelope), envelope
	}

	t.Run("shell state persists across commands", func(t *testing.T) {
		if _, result, _ := run(t, "cd /tmp && export X=42 && f() { echo fn; }", 10); result.ExitCode != 0 {
			t.Fatalf("setup = %+v", result)
		}
		_, result, _ := run(t, "pwd; echo $X $START; f", 10)
		if result.Stdout != "/tmp\n42 yes\nfn\n" {
			t.Fatalf("stdout = %q", result.Stdout)
		}
	})

	t.Run("exit codes and syntax errors do not end the session", func(t *testing.T) {
		if _, result, _ := run(t, "false", 10); result.ExitCode != 1 {
			t.Fatalf("exit = %d", result.ExitCode)
		}
		if _, result, _ := run(t, "if then", 10); result.ExitCode == 0 || result.Stderr == "" {
			t.Fatalf("syntax error = %+v", result)
		}
		if status, result, envelope := run(t, "echo alive; cat", 10); result.Stdout != "alive\n" {
			t.Fatalf("after error: status %d, %+v, %+v", status, result, envelope)
		}
	})

	t.Run("timeout terminates the session", func(t *testing.T) {
		if _, result, _ := run(t, "sleep 60", 1); !result.TimedOut {
			t.Fatalf("result = %+v", result)
		}
		status, _, envelope := run(t, "true", 5)
		if status != http.StatusConflict || envelope.ErrorCode != "session_closed" {
			t.Fatalf("after timeout: status %d, %+v", status, envelope)
		}
	})

	t.Run("sessions can be closed", func(t *testing.T) {
		status, envelope := s.call(t, http.MethodDelete, base+"/"+session.ID, nil)
		requireStatus(t, status, http.StatusOK, envelope)
		status, envelope = s.call(t, http.MethodDelete, base+"/"+session.ID, nil)
		requireStatus(t, status, http.StatusNotFound, envelope)
	})
}
