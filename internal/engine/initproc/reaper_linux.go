//go:build linux

package initproc

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// reaper is the only code in init that calls wait: as PID 1 it must reap
// orphans, and a second waiter (os/exec) would race it for exit statuses.
// spawn holds the lock across fork and registration, so an exit observed
// before registration is still delivered to the right waiter.
type reaper struct {
	mu      sync.Mutex
	waiters map[int]chan unix.WaitStatus
}

func newReaper() *reaper {
	return &reaper{waiters: make(map[int]chan unix.WaitStatus)}
}

func (r *reaper) spawn(argv0 string, argv []string, attr *syscall.ProcAttr) (int, <-chan unix.WaitStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pid, err := syscall.ForkExec(argv0, argv, attr) // #nosec G204 -- running caller-supplied commands is the purpose of a sandbox.
	if err != nil {
		return 0, nil, err
	}
	ch := make(chan unix.WaitStatus, 1)
	r.waiters[pid] = ch
	return pid, ch, nil
}

func (r *reaper) run() {
	signals := make(chan os.Signal, 64)
	signal.Notify(signals, unix.SIGCHLD)
	// SIGCHLD can coalesce; the ticker is a safety net.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-signals:
		case <-ticker.C:
		}
		r.reapAll()
	}
}

func (r *reaper) reapAll() {
	for {
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if pid <= 0 || err != nil {
			return
		}
		r.mu.Lock()
		ch := r.waiters[pid]
		delete(r.waiters, pid)
		r.mu.Unlock()
		if ch != nil {
			ch <- status
		}
	}
}
