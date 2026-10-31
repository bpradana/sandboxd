//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine/agent"
	"github.com/bpradana/sandboxd/internal/engine/fsops"
)

// replyGrace is how long the daemon waits beyond a command's own timeout for
// init to report it, before giving up on the connection.
const replyGrace = 15 * time.Second

// terminalType is the TERM of interactive terminals; browser terminal
// emulators (xterm.js) implement it.
const terminalType = "xterm-256color"

var defaultEnv = map[string]string{
	"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	"LANG": "C.UTF-8",
	"TERM": "dumb",
}

// identity is who a command runs as, in the sandbox's user namespace.
type identity struct {
	uid, gid   int
	name, home string
}

// identity resolves consts.UserRoot or, for anything else, the unprivileged
// sandbox user.
func (e *linuxEngine) identity(user string) identity {
	if user == consts.UserRoot {
		return identity{uid: 0, gid: 0, name: "root", home: rootHome}
	}
	return identity{uid: e.sandboxCfg.ExecUID, gid: e.sandboxCfg.ExecGID, name: sandboxUser, home: sandboxHome}
}

func (e *linuxEngine) Exec(ctx context.Context, id string, req ExecRequest, out OutputFunc) (*ExecResult, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	select {
	case inst.execSem <- struct{}{}:
		defer func() { <-inst.execSem }()
	default:
		return nil, ErrExecLimit
	}

	user := e.identity(req.User)
	cwd := req.Cwd
	if cwd == "" {
		cwd = user.home
	}
	frame := &agent.Frame{Type: agent.TypeExec, Exec: &agent.ExecSpec{
		Argv:      req.Argv,
		UID:       user.uid,
		GID:       user.gid,
		Env:       composeEnv(user, req.Env),
		Cwd:       cwd,
		Stdin:     req.Stdin,
		TimeoutMs: req.Timeout.Milliseconds(),
	}}
	return e.roundTrip(ctx, inst, frame, req.Timeout, out)
}

func (e *linuxEngine) OpenSession(ctx context.Context, id, sessionID string, req SessionRequest) error {
	inst, err := e.running(id)
	if err != nil {
		return err
	}
	inst.sessionsMu.Lock()
	if len(inst.sessions) >= e.execCfg.MaxSessions {
		inst.sessionsMu.Unlock()
		return ErrSessionLimit
	}
	inst.sessions[sessionID] = struct{}{}
	inst.sessionsMu.Unlock()

	user := e.identity(req.User)
	cwd := req.Cwd
	if cwd == "" {
		cwd = user.home
	}
	frame := &agent.Frame{Type: agent.TypeSessionOpen, Session: &agent.SessionSpec{
		ID: sessionID, Shell: req.Shell, UID: user.uid, GID: user.gid, Env: composeEnv(user, req.Env), Cwd: cwd,
	}}
	if _, err := e.roundTrip(ctx, inst, frame, 0, nil); err != nil {
		inst.forgetSession(sessionID)
		return err
	}
	return nil
}

func (e *linuxEngine) SessionExec(ctx context.Context, id, sessionID string, req SessionExecRequest, out OutputFunc) (*ExecResult, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	frame := &agent.Frame{Type: agent.TypeSessionExec, Session: &agent.SessionSpec{
		ID: sessionID, Command: req.Command, TimeoutMs: req.Timeout.Milliseconds(),
	}}
	result, err := e.roundTrip(ctx, inst, frame, req.Timeout, out)
	if errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrSessionNotFound) || (result != nil && result.TimedOut) {
		// Init has already discarded the shell; free the slot.
		inst.forgetSession(sessionID)
	}
	return result, err
}

func (e *linuxEngine) CloseSession(ctx context.Context, id, sessionID string) error {
	inst, err := e.running(id)
	if err != nil {
		return err
	}
	defer inst.forgetSession(sessionID)
	frame := &agent.Frame{Type: agent.TypeSessionClose, Session: &agent.SessionSpec{ID: sessionID}}
	_, err = e.roundTrip(ctx, inst, frame, 0, nil)
	return err
}

func (i *instance) forgetSession(sessionID string) {
	i.sessionsMu.Lock()
	delete(i.sessions, sessionID)
	i.sessionsMu.Unlock()
}

// roundTrip sends one request to init and consumes the reply stream. Output
// frames go to out; the call ends at an exit, ok, or error frame. If ctx ends
// or out fails, the connection is closed, which makes init kill the command.
func (e *linuxEngine) roundTrip(ctx context.Context, inst *instance, request *agent.Frame, timeout time.Duration, out OutputFunc) (*ExecResult, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout+replyGrace)
	defer cancel()

	conn, closeConn, err := dialInit(ctx, inst)
	if err != nil {
		return nil, err
	}
	defer closeConn()
	if err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("send request to sandbox init: %w", err)
	}
	return readReplies(ctx, conn, out)
}

// dialInit connects to init's agent socket. The connection is closed when ctx
// ends or when the returned func is called.
func dialInit(ctx context.Context, inst *instance) (*agent.Conn, func(), error) {
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "unix", inst.socketPath())
	if err != nil {
		return nil, nil, fmt.Errorf("connect to sandbox init: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	return agent.NewConn(raw), func() {
		stop()
		_ = raw.Close()
	}, nil
}

// readReplies consumes init's reply stream up to an exit, ok, or error frame.
func readReplies(ctx context.Context, conn *agent.Conn, out OutputFunc) (*ExecResult, error) {
	for {
		frame, err := conn.Read()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("sandbox init connection: %w", err)
		}
		switch frame.Type {
		case agent.TypeStdout, agent.TypeStderr:
			if out == nil {
				continue
			}
			stream := Stdout
			if frame.Type == agent.TypeStderr {
				stream = Stderr
			}
			if err := out(stream, frame.Data); err != nil {
				_ = conn.Write(&agent.Frame{Type: agent.TypeCancel})
				return nil, err
			}
		case agent.TypeExit:
			if frame.Exit == nil {
				return nil, errors.New("sandbox init sent an empty exit frame")
			}
			return &ExecResult{
				ExitCode: frame.Exit.Code,
				Signal:   frame.Exit.Signal,
				TimedOut: frame.Exit.TimedOut,
				Duration: time.Duration(frame.Exit.DurationMs) * time.Millisecond,
			}, nil
		case agent.TypeOK:
			return nil, nil
		case agent.TypeError:
			return nil, agentError(frame)
		default:
			return nil, fmt.Errorf("unexpected frame %q from sandbox init", frame.Type)
		}
	}
}

func (e *linuxEngine) Terminal(ctx context.Context, id string, req TerminalRequest, input <-chan TerminalInput, out TerminalOutputFunc) (*ExecResult, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}

	// A terminal has no timeout: it lasts as long as ctx, the command, and
	// the input stream. Init enforces the per-sandbox terminal cap, since
	// detached terminals count too.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, closeConn, err := dialInit(ctx, inst)
	if err != nil {
		return nil, err
	}
	defer closeConn()

	request := &agent.Frame{Type: agent.TypeTTYAttach, TTY: &agent.TTYSpec{ID: req.ID, Cols: req.Cols, Rows: req.Rows}}
	if !req.Attach {
		user := e.identity(req.User)
		cwd := req.Cwd
		if cwd == "" {
			cwd = user.home
		}
		env := make(map[string]string, len(req.Env)+1)
		env["TERM"] = terminalType
		for key, value := range req.Env {
			env[key] = value
		}
		request = &agent.Frame{Type: agent.TypeTTYOpen, TTY: &agent.TTYSpec{
			ID: req.ID, Argv: req.Argv, Command: req.Command, UID: user.uid, GID: user.gid, Env: composeEnv(user, env), Cwd: cwd, Cols: req.Cols, Rows: req.Rows,
		}}
	}
	if err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("send request to sandbox init: %w", err)
	}
	reply, err := conn.Read()
	if err != nil {
		return nil, fmt.Errorf("sandbox init connection: %w", err)
	}
	switch reply.Type {
	case agent.TypeOK:
	case agent.TypeError:
		return nil, agentError(reply)
	default:
		return nil, fmt.Errorf("unexpected frame %q from sandbox init", reply.Type)
	}
	if req.Ready != nil {
		if err := req.Ready(); err != nil {
			return nil, err
		}
	}

	var detached atomic.Bool
	go forwardTerminalInput(ctx, conn, input, func() {
		detached.Store(true)
		cancel()
	})
	result, err := readReplies(ctx, conn, func(_ Stream, data []byte) error { return out(data) })
	if detached.Load() {
		return nil, ErrTerminalDetached
	}
	return result, err
}

// forwardTerminalInput relays input until ctx ends. A closed input stream
// detaches: the connection is dropped and init keeps the terminal running.
func forwardTerminalInput(ctx context.Context, conn *agent.Conn, input <-chan TerminalInput, detach func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case in, ok := <-input:
			if !ok {
				detach()
				return
			}
			var err error
			if len(in.Data) > 0 {
				err = conn.Write(&agent.Frame{Type: agent.TypeStdin, Data: in.Data})
			}
			if err == nil && in.Cols > 0 && in.Rows > 0 {
				err = conn.Write(&agent.Frame{Type: agent.TypeResize, TTY: &agent.TTYSpec{Cols: in.Cols, Rows: in.Rows}})
			}
			if err == nil && in.Close {
				err = conn.Write(&agent.Frame{Type: agent.TypeCancel})
			}
			if err != nil {
				return
			}
		}
	}
}

func agentError(frame *agent.Frame) error {
	switch frame.Code {
	case agent.CodeSessionNotFound:
		return ErrSessionNotFound
	case agent.CodeSessionBusy:
		return ErrSessionBusy
	case agent.CodeSessionClosed:
		return ErrSessionClosed
	case agent.CodeTerminalNotFound:
		return ErrTerminalNotFound
	case agent.CodeTerminalLimit:
		return ErrTerminalLimit
	case agent.CodeTerminalTaken:
		return ErrTerminalTaken
	default:
		return fmt.Errorf("sandbox init: %s: %s", frame.Code, frame.Message)
	}
}

// composeEnv layers the request environment over the defaults and the
// user's HOME, USER, and LOGNAME, sorted for deterministic process
// environments.
func composeEnv(user identity, overrides map[string]string) []string {
	merged := make(map[string]string, len(defaultEnv)+len(overrides)+3)
	for key, value := range defaultEnv {
		merged[key] = value
	}
	merged["HOME"], merged["USER"], merged["LOGNAME"] = user.home, user.name, user.name
	for key, value := range overrides {
		merged[key] = value
	}
	env := make([]string, 0, len(merged))
	for key, value := range merged {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

// root opens the sandbox filesystem for a file operation. New entries are
// owned by the sandbox user.
type lockedRoot struct {
	*fsops.Root
	unlock func()
}

func (r *lockedRoot) Close() error {
	defer r.unlock()
	return r.Root.Close()
}

func (e *linuxEngine) root(id string) (*lockedRoot, error) {
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	inst.fsMu.RLock()
	if inst.destroying.Load() {
		inst.fsMu.RUnlock()
		return nil, ErrSandboxNotFound
	}
	root, err := fsops.OpenRoot(inst.rootfsDir(), e.sandboxCfg.UIDMapBase+e.sandboxCfg.ExecUID, e.sandboxCfg.UIDMapBase+e.sandboxCfg.ExecGID)
	if err != nil {
		inst.fsMu.RUnlock()
		return nil, err
	}
	return &lockedRoot{Root: root, unlock: inst.fsMu.RUnlock}, nil
}

func (e *linuxEngine) OpenFile(_ context.Context, id, path string) (io.ReadCloser, *FileInfo, error) {
	root, err := e.root(id)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	file, info, err := root.Open(path)
	if err != nil {
		return nil, nil, mapFSError(err)
	}
	converted := fileInfo(*info)
	return file, &converted, nil
}

func (e *linuxEngine) WriteFile(_ context.Context, id, path string, mode os.FileMode, r io.Reader, maxBytes int64) (*FileInfo, error) {
	root, err := e.root(id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.WriteFile(path, mode, r, maxBytes)
	if err != nil {
		return nil, mapFSError(err)
	}
	converted := fileInfo(*info)
	return &converted, nil
}

func (e *linuxEngine) ListDir(_ context.Context, id, path string) ([]FileInfo, error) {
	root, err := e.root(id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	entries, err := root.ReadDir(path)
	if err != nil {
		return nil, mapFSError(err)
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, fileInfo(entry))
	}
	return result, nil
}

func (e *linuxEngine) Stat(_ context.Context, id, path string) (*FileInfo, error) {
	root, err := e.root(id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Stat(path)
	if err != nil {
		return nil, mapFSError(err)
	}
	converted := fileInfo(*info)
	return &converted, nil
}

func (e *linuxEngine) Mkdir(_ context.Context, id, path string, mode os.FileMode, parents bool) (*FileInfo, error) {
	root, err := e.root(id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Mkdir(path, mode, parents)
	if err != nil {
		return nil, mapFSError(err)
	}
	converted := fileInfo(*info)
	return &converted, nil
}

func (e *linuxEngine) Remove(_ context.Context, id, path string, recursive bool) error {
	root, err := e.root(id)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return mapFSError(root.Remove(path, recursive))
}

func fileInfo(info fsops.Info) FileInfo {
	fileType := consts.FileTypeOther
	switch {
	case info.Mode.IsRegular():
		fileType = consts.FileTypeFile
	case info.Mode.IsDir():
		fileType = consts.FileTypeDir
	case info.Mode&os.ModeSymlink != 0:
		fileType = consts.FileTypeSymlink
	}
	return FileInfo{
		Path:       info.Path,
		Name:       info.Name,
		Type:       fileType,
		Size:       info.Size,
		Mode:       info.Mode,
		ModTime:    info.ModTime,
		LinkTarget: info.LinkTarget,
	}
}

// mapFSError translates errno values from confined file operations. EXDEV
// and ELOOP are what openat2 returns when a path tries to leave the root
// through a mount crossing or magic link.
func mapFSError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fsops.ErrTooLarge):
		return ErrFileTooLarge
	case errors.Is(err, fsops.ErrNotRegular):
		return ErrInvalidPath
	case errors.Is(err, unix.ENOENT):
		return ErrPathNotFound
	case errors.Is(err, unix.EISDIR):
		return ErrIsDirectory
	case errors.Is(err, unix.ENOTDIR):
		return ErrNotDirectory
	case errors.Is(err, unix.EEXIST):
		return ErrExists
	case errors.Is(err, unix.ENOTEMPTY):
		return ErrDirectoryNotEmpty
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return ErrNoSpace
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ELOOP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENAMETOOLONG):
		return ErrInvalidPath
	default:
		return err
	}
}
