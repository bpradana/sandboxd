//go:build linux

package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func (e *linuxEngine) DialSandbox(ctx context.Context, id string, port int) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid preview port")
	}
	inst, err := e.running(id)
	if err != nil {
		return nil, err
	}
	inst.teardownMu.Lock()
	if inst.destroying.Load() {
		inst.teardownMu.Unlock()
		return nil, ErrSandboxNotFound
	}
	ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", inst.pid)) // #nosec G304 -- engine-owned init pid, not user input.
	inst.teardownMu.Unlock()
	if err != nil {
		return nil, err
	}
	defer func() { _ = ns.Close() }()
	fd, err := socketInNamespace(int(ns.Fd())) // #nosec G115 -- valid file descriptor.
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "sandbox-preview") // #nosec G115 -- valid socket descriptor.
	defer func() { _ = file.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err = unix.Connect(fd, &unix.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}})
	if err != nil && !errors.Is(err, unix.EINPROGRESS) {
		return nil, err
	}
	for err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}} // #nosec G115 -- bounded process file descriptor.
		n, pollErr := unix.Poll(fds, 50)
		if errors.Is(pollErr, unix.EINTR) || n == 0 && pollErr == nil {
			continue
		}
		if pollErr != nil {
			return nil, pollErr
		}
		code, sockErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
		if sockErr != nil {
			return nil, sockErr
		}
		if code != 0 {
			return nil, unix.Errno(code)
		}
		err = nil
	}
	return net.FileConn(file)
}

// Bind the socket to the sandbox netns before connecting. net.DialContext can
// create sockets on another goroutine, so calling it under setns is unsafe.
// Only this short-lived locked thread enters the netns. If restoring the host
// namespace fails, return without unlocking; Go discards that OS thread.
func socketInNamespace(target int) (int, error) {
	type result struct {
		fd  int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()
		original, err := os.Open("/proc/thread-self/ns/net")
		if err != nil {
			runtime.UnlockOSThread()
			ch <- result{-1, err}
			return
		}
		defer func() { _ = original.Close() }()
		if err := unix.Setns(target, unix.CLONE_NEWNET); err != nil {
			runtime.UnlockOSThread()
			ch <- result{-1, err}
			return
		}
		fd, socketErr := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
		if err := unix.Setns(int(original.Fd()), unix.CLONE_NEWNET); err != nil { // #nosec G115 -- valid file descriptor.
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			ch <- result{-1, err}
			return
		}
		runtime.UnlockOSThread()
		ch <- result{fd, socketErr}
	}()
	r := <-ch
	return r.fd, r.err
}
