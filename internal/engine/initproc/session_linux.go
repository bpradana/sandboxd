//go:build linux

package initproc

import (
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/engine/agent"
)

// session is a long-lived shell. Its stdout/stderr are read continuously into
// channels so the shell never blocks on a full pipe between commands.
type session struct {
	proc   *process
	stdout chan []byte
	stderr chan []byte
	exited chan struct{}
	busy   sync.Mutex

	closeOnce sync.Once
}

func (s *server) openSession(conn *agent.Conn, spec *agent.SessionSpec) {
	if spec == nil || spec.ID == "" || !validIdentity(spec.UID, spec.GID) {
		writeError(conn, agent.CodeBadRequest, "session id and a valid identity are required")
		return
	}

	s.mu.Lock()
	if _, exists := s.sessions[spec.ID]; exists {
		s.mu.Unlock()
		writeError(conn, agent.CodeSessionExists, "session already exists")
		return
	}
	s.mu.Unlock()

	shell := "/bin/sh"
	if spec.Shell == "bash" {
		if _, err := os.Stat("/bin/bash"); err == nil {
			shell = "/bin/bash"
		}
	}
	proc, err := s.spawn([]string{shell}, spec.Env, spec.Cwd, spec.UID, spec.GID)
	if err != nil {
		writeError(conn, agent.CodeSpawnFailed, err.Error())
		return
	}

	sess := &session{
		proc:   proc,
		stdout: make(chan []byte, 256),
		stderr: make(chan []byte, 256),
		exited: make(chan struct{}),
	}
	go sess.read(proc.stdout, sess.stdout)
	go sess.read(proc.stderr, sess.stderr)
	go func() {
		<-proc.exit
		close(sess.exited)
		s.removeSession(spec.ID, sess)
	}()

	s.mu.Lock()
	s.sessions[spec.ID] = sess
	s.mu.Unlock()
	_ = conn.Write(&agent.Frame{Type: agent.TypeOK})
}

func (sess *session) read(src *os.File, dst chan<- []byte) {
	defer close(dst)
	buf := make([]byte, agent.OutputChunkSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			dst <- append([]byte(nil), buf[:n]...)
		}
		if err != nil {
			return
		}
	}
}

func (sess *session) terminate() {
	sess.closeOnce.Do(func() {
		sess.proc.killGroup()
		_ = sess.proc.stdin.Close()
	})
}

func (s *server) removeSession(id string, sess *session) {
	s.mu.Lock()
	if s.sessions[id] == sess {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	sess.terminate()
}

func (s *server) lookupSession(id string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *server) sessionExec(conn *agent.Conn, spec *agent.SessionSpec) {
	if spec == nil {
		writeError(conn, agent.CodeBadRequest, "session spec is required")
		return
	}
	sess := s.lookupSession(spec.ID)
	if sess == nil {
		writeError(conn, agent.CodeSessionNotFound, "session not found")
		return
	}
	if !sess.busy.TryLock() {
		writeError(conn, agent.CodeSessionBusy, "session is running another command")
		return
	}
	// The session is released before the exit frame is written: the daemon
	// answers its client on that frame, and the client's next command must
	// not find the session still busy.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(sess.busy.Unlock) }
	defer release()

	select {
	case <-sess.exited:
		writeError(conn, agent.CodeSessionClosed, "session shell has exited")
		return
	default:
	}
	discardPending(sess.stdout)
	discardPending(sess.stderr)

	started := time.Now()
	token := randomToken()
	outMarker, errMarker := "__SBX_"+token+"_", "__SBX_"+token+"__"
	// `command eval` keeps syntax errors from terminating the shell, stdin
	// from /dev/null keeps commands from consuming the protocol stream, and
	// the markers report the exit status on both streams.
	script := "command eval " + shellQuote(spec.Command) + " </dev/null\n" +
		"__sbx_rc=$?; printf '\\n" + outMarker + "%d\\n' \"$__sbx_rc\"; printf '\\n" + errMarker + "0\\n' >&2\n"
	if _, err := sess.proc.stdin.WriteString(script); err != nil {
		s.removeSession(spec.ID, sess)
		writeError(conn, agent.CodeSessionClosed, "session shell has exited")
		return
	}

	canceled := make(chan struct{})
	go func() {
		for {
			frame, err := conn.Read()
			if err != nil || frame.Type == agent.TypeCancel {
				close(canceled)
				return
			}
		}
	}()

	outScan, errScan := newMarkerScanner(outMarker), newMarkerScanner(errMarker)
	timer := time.NewTimer(time.Duration(spec.TimeoutMs) * time.Millisecond)
	defer timer.Stop()

	stdout, stderr := sess.stdout, sess.stderr
	for !outScan.Done() || !errScan.Done() {
		select {
		case chunk, ok := <-stdout:
			if !ok {
				stdout = nil
				forward(conn, agent.TypeStdout, outScan.Flush())
				break
			}
			forward(conn, agent.TypeStdout, outScan.Feed(chunk))
		case chunk, ok := <-stderr:
			if !ok {
				stderr = nil
				forward(conn, agent.TypeStderr, errScan.Flush())
				break
			}
			forward(conn, agent.TypeStderr, errScan.Feed(chunk))
		case <-timer.C:
			// The shell's state is unknown after a kill, so the session ends.
			s.removeSession(spec.ID, sess)
			_ = conn.Write(&agent.Frame{Type: agent.TypeExit, Exit: &agent.ExitStatus{
				Code: 128 + int(unix.SIGKILL), Signal: "SIGKILL", TimedOut: true, DurationMs: time.Since(started).Milliseconds(),
			}})
			return
		case <-canceled:
			s.removeSession(spec.ID, sess)
			return
		}
		if stdout == nil && stderr == nil {
			s.removeSession(spec.ID, sess)
			writeError(conn, agent.CodeSessionClosed, "session shell exited during the command")
			return
		}
	}

	release()
	_ = conn.Write(&agent.Frame{Type: agent.TypeExit, Exit: &agent.ExitStatus{
		Code: outScan.Code(), DurationMs: time.Since(started).Milliseconds(),
	}})
}

func (s *server) closeSession(conn *agent.Conn, spec *agent.SessionSpec) {
	if spec == nil {
		writeError(conn, agent.CodeBadRequest, "session spec is required")
		return
	}
	sess := s.lookupSession(spec.ID)
	if sess == nil {
		writeError(conn, agent.CodeSessionNotFound, "session not found")
		return
	}
	s.removeSession(spec.ID, sess)
	_ = conn.Write(&agent.Frame{Type: agent.TypeOK})
}

func forward(conn *agent.Conn, frameType string, data []byte) {
	if len(data) > 0 {
		_ = conn.Write(&agent.Frame{Type: frameType, Data: data})
	}
}

func discardPending(ch <-chan []byte) {
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}
