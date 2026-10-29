//go:build linux

package initproc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/engine/agent"
	"github.com/bpradana/sandboxd/internal/engine/reexec"
)

// outputGrace is how long init keeps reading output after a command's
// process group is gone, for data still buffered in the pipes. Processes that
// escaped the group (setsid) and still hold the pipes are cut off after it.
const outputGrace = 2 * time.Second

type server struct {
	cfg    *Config
	reaper *reaper

	mu        sync.Mutex
	sessions  map[string]*session
	terminals map[string]*terminal
}

func newServer(cfg *Config) *server {
	return &server{cfg: cfg, reaper: newReaper(), sessions: make(map[string]*session), terminals: make(map[string]*terminal)}
}

func (s *server) serve(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.handle(conn)
	}
}

func (s *server) handle(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	conn := agent.NewConn(raw)
	frame, err := conn.Read()
	if err != nil {
		return
	}

	switch frame.Type {
	case agent.TypeExec:
		if frame.Exec == nil || len(frame.Exec.Argv) == 0 || !validIdentity(frame.Exec.UID, frame.Exec.GID) {
			writeError(conn, agent.CodeBadRequest, "exec requires argv and a valid identity")
			return
		}
		s.exec(conn, frame.Exec)
	case agent.TypeSessionOpen:
		s.openSession(conn, frame.Session)
	case agent.TypeSessionExec:
		s.sessionExec(conn, frame.Session)
	case agent.TypeSessionClose:
		s.closeSession(conn, frame.Session)
	case agent.TypeTTYOpen, agent.TypeTTYAttach:
		if frame.TTY == nil || !validIdentity(frame.TTY.UID, frame.TTY.GID) {
			writeError(conn, agent.CodeBadRequest, "tty requires a spec with a valid identity")
			return
		}
		if frame.Type == agent.TypeTTYOpen {
			s.openTTY(raw, conn, frame.TTY)
		} else {
			s.attachTTY(raw, conn, frame.TTY)
		}
	default:
		writeError(conn, agent.CodeBadRequest, "unknown request type "+frame.Type)
	}
}

// process is a spawned command with its stdio pipes.
type process struct {
	pid    int
	exit   <-chan unix.WaitStatus
	stdin  *os.File
	stdout *os.File
	stderr *os.File
}

// spawn starts argv through the shim as uid/gid in a new session (so pid ==
// pgid and the whole group can be killed).
func (s *server) spawn(argv, env []string, cwd string, uid, gid int) (*process, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW)
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeAll(stdinR, stdinW, stdoutR, stdoutW)
		return nil, err
	}

	pid, exit, err := s.spawnShim(argv, env, cwd, uid, gid, []uintptr{stdinR.Fd(), stdoutW.Fd(), stderrW.Fd()}, false)
	closeAll(stdinR, stdoutW, stderrW)
	if err != nil {
		closeAll(stdinW, stdoutR, stderrR)
		return nil, err
	}
	return &process{pid: pid, exit: exit, stdin: stdinW, stdout: stdoutR, stderr: stderrR}, nil
}

// spawnShim forks the shim as uid/gid with files as its stdio. With ctty,
// stdin (child fd 0) becomes the controlling terminal of the new session.
// As uid 0 the command holds the capabilities left in init's bounding set;
// any other uid holds none.
func (s *server) spawnShim(argv, env []string, cwd string, uid, gid int, files []uintptr, ctty bool) (int, <-chan unix.WaitStatus, error) {
	seccompFlag := "0"
	if s.cfg.Seccomp {
		seccompFlag = "1"
	}
	shimArgv := append([]string{reexec.ShimName, cwd, seccompFlag, "--"}, argv...)
	return s.reaper.spawn("/proc/self/exe", shimArgv, &syscall.ProcAttr{
		Env:   env,
		Files: files,
		Sys: &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: ctty,
			Ctty:    0,
			Credential: &syscall.Credential{
				Uid:    uint32(uid),           // #nosec G115 -- checked by validIdentity.
				Gid:    uint32(gid),           // #nosec G115 -- checked by validIdentity.
				Groups: []uint32{uint32(gid)}, // #nosec G115 -- checked by validIdentity.
			},
		},
	})
}

func (p *process) killGroup() {
	_ = unix.Kill(-p.pid, unix.SIGKILL)
}

func (s *server) exec(conn *agent.Conn, spec *agent.ExecSpec) {
	started := time.Now()
	proc, err := s.spawn(spec.Argv, spec.Env, spec.Cwd, spec.UID, spec.GID)
	if err != nil {
		writeError(conn, agent.CodeSpawnFailed, err.Error())
		return
	}

	go func() {
		if len(spec.Stdin) > 0 {
			_, _ = proc.stdin.Write(spec.Stdin)
		}
		_ = proc.stdin.Close()
	}()

	var pumps sync.WaitGroup
	pumps.Add(2)
	go pump(&pumps, proc.stdout, conn, agent.TypeStdout)
	go pump(&pumps, proc.stderr, conn, agent.TypeStderr)

	// The daemon closes the connection (or sends cancel) when its client goes
	// away; either way the command must die.
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

	timeout := time.Duration(spec.TimeoutMs) * time.Millisecond
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	timedOut := false
	var status unix.WaitStatus
	select {
	case status = <-proc.exit:
	case <-timer.C:
		timedOut = true
		proc.killGroup()
		status = <-proc.exit
	case <-canceled:
		proc.killGroup()
		status = <-proc.exit
	}
	// Background jobs left in the command's group end with it.
	proc.killGroup()

	drainPumps(&pumps, proc.stdout, proc.stderr)
	exit := exitStatus(status, timedOut, time.Since(started))
	_ = conn.Write(&agent.Frame{Type: agent.TypeExit, Exit: exit})
}

// pump forwards a pipe to the connection in chunks.
func pump(wg *sync.WaitGroup, src *os.File, conn *agent.Conn, frameType string) {
	defer wg.Done()
	buf := make([]byte, agent.OutputChunkSize)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if writeErr := conn.Write(&agent.Frame{Type: frameType, Data: append([]byte(nil), buf[:n]...)}); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// drainPumps waits for buffered output, then forcibly closes the pipes.
func drainPumps(wg *sync.WaitGroup, files ...*os.File) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(outputGrace):
		closeAll(files...)
		<-done
	}
	closeAll(files...)
}

func exitStatus(status unix.WaitStatus, timedOut bool, elapsed time.Duration) *agent.ExitStatus {
	exit := &agent.ExitStatus{TimedOut: timedOut, DurationMs: elapsed.Milliseconds()}
	switch {
	case status.Exited():
		exit.Code = status.ExitStatus()
	case status.Signaled():
		exit.Code = 128 + int(status.Signal())
		exit.Signal = signalName(status.Signal())
	default:
		exit.Code = -1
	}
	return exit
}

func signalName(sig unix.Signal) string {
	if name := unix.SignalName(sig); name != "" {
		return name
	}
	return "SIG" + strconv.Itoa(int(sig))
}

func writeError(conn *agent.Conn, code, message string) {
	_ = conn.Write(&agent.Frame{Type: agent.TypeError, Code: code, Message: message})
}

func closeAll(files ...*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

// validIdentity bounds uids and gids to the sandbox's mapped range.
func validIdentity(uid, gid int) bool {
	return uid >= 0 && uid < 65536 && gid >= 0 && gid < 65536
}

func randomToken() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
