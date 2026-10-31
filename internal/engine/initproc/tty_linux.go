//go:build linux

package initproc

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/engine/agent"
)

const (
	// ttyInputTimeout bounds one write of keyboard input to the terminal. A
	// program that stops reading its terminal must not stall the frame
	// reader, which also watches for detach and cancel; input it does not
	// take is dropped.
	ttyInputTimeout = 10 * time.Second
	// ttyScrollback is how much recent output a reattaching client gets.
	ttyScrollback = 256 << 10
	// ttyDefaultDetach applies when the daemon sends no detach window.
	ttyDefaultDetach = 10 * time.Minute
)

// terminal is an interactive command on a pseudo-terminal. It outlives the
// connection that opened it: a client that goes away detaches, and the shell
// keeps running for the detach window so a new connection can reattach.
type terminal struct {
	id     string
	master *os.File
	proc   *process
	// detachWindow is how long the terminal survives with no client.
	detachWindow time.Duration

	mu       sync.Mutex
	recent   []byte // the last ttyScrollback bytes of output
	attached *ttyClient
	detached *time.Timer
	ended    bool
}

// ttyClient is one attached connection.
type ttyClient struct {
	conn *agent.Conn
	raw  net.Conn
}

// openPTY allocates a pseudo-terminal pair from the sandbox's private devpts
// instance. The peer is obtained from the master with TIOCGPTPEER rather than
// by path, and is owned by the terminal's user so programs can reopen
// /dev/tty.
func (s *server) openPTY(cols, rows uint16, uid, gid int) (*os.File, *os.File, error) {
	// Non-blocking, so os.File uses the poller and input writes can time out.
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open ptmx: %w", err)
	}
	master := os.NewFile(uintptr(masterFD), "/dev/ptmx")
	if err := unix.IoctlSetPointerInt(masterFD, unix.TIOCSPTLCK, 0); err != nil {
		_ = master.Close()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	peerFD, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(masterFD), unix.TIOCGPTPEER, uintptr(unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC))
	if errno != 0 {
		_ = master.Close()
		return nil, nil, fmt.Errorf("open pty peer: %w", errno)
	}
	peer := os.NewFile(peerFD, "pty")
	if err := unix.Fchown(int(peerFD), uid, gid); err != nil {
		closeAll(master, peer)
		return nil, nil, fmt.Errorf("chown pty: %w", err)
	}
	if err := setWinsize(master, cols, rows); err != nil {
		closeAll(master, peer)
		return nil, nil, err
	}
	return master, peer, nil
}

func setWinsize(master *os.File, cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	raw, err := master.SyscallConn()
	if err != nil {
		return err
	}
	var ioctlErr error
	if err := raw.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
	}); err != nil {
		return err
	}
	if ioctlErr != nil {
		return fmt.Errorf("set terminal size: %w", ioctlErr)
	}
	return nil
}

// openTTY starts a new terminal and attaches the connection to it.
func (s *server) openTTY(raw net.Conn, conn *agent.Conn, spec *agent.TTYSpec) {
	if spec.ID == "" {
		writeError(conn, agent.CodeBadRequest, "terminal id is required")
		return
	}
	s.mu.Lock()
	if _, exists := s.terminals[spec.ID]; exists {
		s.mu.Unlock()
		writeError(conn, agent.CodeSessionExists, "terminal already exists")
		return
	}
	if len(s.terminals) >= s.maxTerminals() {
		s.mu.Unlock()
		writeError(conn, agent.CodeTerminalLimit, "too many terminals")
		return
	}
	// Reserve the id while the shell starts.
	s.terminals[spec.ID] = nil
	s.mu.Unlock()

	term, err := s.startTerminal(spec)
	s.mu.Lock()
	if err != nil {
		delete(s.terminals, spec.ID)
	} else {
		s.terminals[spec.ID] = term
	}
	s.mu.Unlock()
	if err != nil {
		writeError(conn, agent.CodeSpawnFailed, err.Error())
		return
	}
	s.serveTerminal(term, raw, conn, spec.Cols, spec.Rows)
}

// attachTTY reattaches the connection to a running terminal.
func (s *server) attachTTY(raw net.Conn, conn *agent.Conn, spec *agent.TTYSpec) {
	s.mu.Lock()
	term := s.terminals[spec.ID]
	s.mu.Unlock()
	if term == nil {
		writeError(conn, agent.CodeTerminalNotFound, "terminal not found")
		return
	}
	s.serveTerminal(term, raw, conn, spec.Cols, spec.Rows)
}

func (s *server) maxTerminals() int {
	if s.cfg.MaxTerminals > 0 {
		return s.cfg.MaxTerminals
	}
	return 4
}

func (s *server) detachWindow() time.Duration {
	if s.cfg.TerminalDetachMs > 0 {
		return time.Duration(s.cfg.TerminalDetachMs) * time.Millisecond
	}
	return ttyDefaultDetach
}

func (s *server) startTerminal(spec *agent.TTYSpec) (*terminal, error) {
	started := time.Now()
	argv := terminalArgv(spec)
	master, peer, err := s.openPTY(spec.Cols, spec.Rows, spec.UID, spec.GID)
	if err != nil {
		return nil, err
	}
	pid, exit, err := s.spawnShim(argv, spec.Env, spec.Cwd, spec.UID, spec.GID, []uintptr{peer.Fd(), peer.Fd(), peer.Fd()}, true)
	_ = peer.Close()
	if err != nil {
		_ = master.Close()
		return nil, err
	}
	term := &terminal{id: spec.ID, master: master, proc: &process{pid: pid, exit: exit}, detachWindow: s.detachWindow()}

	var pumps sync.WaitGroup
	pumps.Add(1)
	go term.pump(&pumps)
	go func() {
		status := <-exit
		// Jobs left in the terminal's session end with its shell.
		term.proc.killGroup()
		drainPumps(&pumps, master)
		s.endTerminal(term, exitStatus(status, false, time.Since(started)))
	}()
	return term, nil
}

// pump records output and relays it to the attached client, if any. Once
// every holder of the peer has closed it, reading the master fails with EIO,
// which ends the pump like EOF.
func (t *terminal) pump(wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, agent.OutputChunkSize)
	for {
		n, err := t.master.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			t.mu.Lock()
			t.recent = append(t.recent, chunk...)
			if excess := len(t.recent) - ttyScrollback; excess > 0 {
				t.recent = append(t.recent[:0:0], t.recent[excess:]...)
			}
			// Written under the lock so output and a reattach's replay
			// never interleave or repeat.
			if client := t.attached; client != nil {
				if client.conn.Write(&agent.Frame{Type: agent.TypeStdout, Data: chunk}) != nil {
					t.detachLocked(client)
				}
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// serveTerminal attaches a connection, replays recent output, and applies
// input until the client detaches or the terminal ends. A newer attachment
// takes the terminal over from an older one.
func (s *server) serveTerminal(term *terminal, raw net.Conn, conn *agent.Conn, cols, rows uint16) {
	client := &ttyClient{conn: conn, raw: raw}

	term.mu.Lock()
	if term.ended {
		term.mu.Unlock()
		writeError(conn, agent.CodeTerminalNotFound, "terminal not found")
		return
	}
	if previous := term.attached; previous != nil {
		_ = previous.conn.Write(&agent.Frame{Type: agent.TypeError, Code: agent.CodeTerminalTaken, Message: "terminal attached elsewhere"})
		_ = previous.raw.Close()
	}
	if term.detached != nil {
		term.detached.Stop()
		term.detached = nil
	}
	_ = setWinsize(term.master, cols, rows)
	if conn.Write(&agent.Frame{Type: agent.TypeOK}) != nil {
		term.detachLocked(nil)
		term.mu.Unlock()
		return
	}
	if len(term.recent) > 0 {
		_ = conn.Write(&agent.Frame{Type: agent.TypeStdout, Data: term.recent})
	}
	term.attached = client
	term.mu.Unlock()

	for {
		frame, err := conn.Read()
		if err != nil {
			term.mu.Lock()
			term.detachLocked(client)
			term.mu.Unlock()
			return
		}
		switch frame.Type {
		case agent.TypeCancel:
			// The exit frame follows once the shell is gone.
			term.proc.killGroup()
		case agent.TypeStdin:
			writeInput(term.master, frame.Data)
		case agent.TypeResize:
			if frame.TTY != nil {
				_ = setWinsize(term.master, frame.TTY.Cols, frame.TTY.Rows)
			}
		}
	}
}

// detachLocked drops client if it is still the attached one (nil: whoever
// is attached) and starts the countdown after which an unattended terminal
// is killed. The caller holds t.mu.
func (t *terminal) detachLocked(client *ttyClient) {
	if client != nil && t.attached != client {
		return
	}
	t.attached = nil
	if t.ended || t.detached != nil {
		return
	}
	t.detached = time.AfterFunc(t.detachWindow, t.proc.killGroup)
}

func (s *server) endTerminal(term *terminal, exit *agent.ExitStatus) {
	term.mu.Lock()
	term.ended = true
	if term.detached != nil {
		term.detached.Stop()
	}
	if client := term.attached; client != nil {
		_ = client.conn.Write(&agent.Frame{Type: agent.TypeExit, Exit: exit})
		term.attached = nil
	}
	term.mu.Unlock()

	s.mu.Lock()
	if s.terminals[term.id] == term {
		delete(s.terminals, term.id)
	}
	s.mu.Unlock()
}

func writeInput(master *os.File, data []byte) {
	if len(data) == 0 {
		return
	}
	_ = master.SetWriteDeadline(time.Now().Add(ttyInputTimeout))
	_, _ = master.Write(data)
}

// terminalArgv is the caller's argv, or the login shell: interactive, or
// running the caller's command and exiting with it.
func terminalArgv(spec *agent.TTYSpec) []string {
	switch {
	case len(spec.Argv) > 0:
		return spec.Argv
	case spec.Command != "":
		return []string{loginShell(), "-lc", spec.Command}
	default:
		return []string{loginShell(), "-l"}
	}
}

// loginShell prefers bash when the image has it.
func loginShell() string {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return "/bin/bash"
	}
	return "/bin/sh"
}
