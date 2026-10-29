// Package agent defines the framed JSON protocol spoken between the daemon and
// the init process inside each sandbox over a unix socket. Frames are a 4-byte
// big-endian length followed by a JSON object. The daemon treats every frame it
// reads as untrusted data: init runs as root inside the sandbox's user
// namespace, which is less trusted than the daemon.
package agent

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// MaxFrameSize bounds a single frame in both directions.
const MaxFrameSize = 4 << 20

// OutputChunkSize is the largest output payload init puts in one frame.
const OutputChunkSize = 32 << 10

// Frame types. Requests flow daemon → init; the rest flow init → daemon,
// except Cancel which the daemon may send while a command runs, and Stdin and
// Resize which it sends while a terminal runs.
const (
	TypeExec         = "exec"
	TypeSessionOpen  = "session_open"
	TypeSessionExec  = "session_exec"
	TypeSessionClose = "session_close"
	TypeTTYOpen      = "tty_open"
	TypeTTYAttach    = "tty_attach"
	TypeCancel       = "cancel"
	TypeStdin        = "stdin"
	TypeResize       = "resize"

	TypeStdout = "stdout"
	TypeStderr = "stderr"
	TypeExit   = "exit"
	TypeOK     = "ok"
	TypeError  = "error"
)

// Error codes carried by TypeError frames.
const (
	CodeSessionNotFound = "session_not_found"
	CodeSessionBusy     = "session_busy"
	CodeSessionClosed   = "session_closed"
	CodeSessionExists   = "session_exists"
	CodeSpawnFailed     = "spawn_failed"
	// Terminal errors: unknown or ended, too many, or taken over by a newer
	// attachment.
	CodeTerminalNotFound = "terminal_not_found"
	CodeTerminalLimit    = "terminal_limit"
	CodeTerminalTaken    = "terminal_taken"
	CodeBadRequest       = "bad_request"
)

type Frame struct {
	Type    string       `json:"type"`
	Exec    *ExecSpec    `json:"exec,omitempty"`
	Session *SessionSpec `json:"session,omitempty"`
	TTY     *TTYSpec     `json:"tty,omitempty"`
	Data    []byte       `json:"data,omitempty"`
	Exit    *ExitStatus  `json:"exit,omitempty"`
	Code    string       `json:"code,omitempty"`
	Message string       `json:"message,omitempty"`
}

// UID and GID in the specs below are the sandbox-namespace identity a
// command runs as.

type ExecSpec struct {
	Argv []string `json:"argv"`
	UID  int      `json:"uid"`
	GID  int      `json:"gid"`
	// Env is the complete environment, KEY=VALUE.
	Env       []string `json:"env"`
	Cwd       string   `json:"cwd"`
	Stdin     []byte   `json:"stdin,omitempty"`
	TimeoutMs int64    `json:"timeout_ms"`
}

type SessionSpec struct {
	ID        string   `json:"id"`
	Shell     string   `json:"shell,omitempty"`
	UID       int      `json:"uid,omitempty"`
	GID       int      `json:"gid,omitempty"`
	Env       []string `json:"env,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	Command   string   `json:"command,omitempty"`
	TimeoutMs int64    `json:"timeout_ms,omitempty"`
}

// TTYSpec opens (TypeTTYOpen) or reattaches to (TypeTTYAttach) an
// interactive command on a pseudo-terminal; init answers TypeOK, then replays
// recent output. On TypeResize frames only Cols and Rows are set. Closing the
// connection detaches; TypeCancel ends the terminal.
type TTYSpec struct {
	ID   string   `json:"id,omitempty"`
	Argv []string `json:"argv,omitempty"`
	UID  int      `json:"uid,omitempty"`
	GID  int      `json:"gid,omitempty"`
	// Env is the complete environment, KEY=VALUE.
	Env  []string `json:"env,omitempty"`
	Cwd  string   `json:"cwd,omitempty"`
	Cols uint16   `json:"cols"`
	Rows uint16   `json:"rows"`
}

type ExitStatus struct {
	Code       int    `json:"code"`
	Signal     string `json:"signal,omitempty"`
	TimedOut   bool   `json:"timed_out"`
	DurationMs int64  `json:"duration_ms"`
}

// Conn reads and writes frames. Writes are serialized so output goroutines can
// share one connection; reads must come from a single goroutine.
type Conn struct {
	rw io.ReadWriter
	mu sync.Mutex
}

func NewConn(rw io.ReadWriter) *Conn {
	return &Conn{rw: rw}
}

func (c *Conn) Write(frame *Frame) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(payload) > MaxFrameSize {
		return fmt.Errorf("frame of %d bytes exceeds limit", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload))) // #nosec G115 -- bounded by MaxFrameSize above.
	copy(buf[4:], payload)

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.rw.Write(buf)
	return err
}

func (c *Conn) Read() (*Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(c.rw, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameSize {
		return nil, fmt.Errorf("invalid frame size %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return nil, err
	}
	var frame Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}
	if frame.Type == "" {
		return nil, errors.New("frame without type")
	}
	return &frame, nil
}
