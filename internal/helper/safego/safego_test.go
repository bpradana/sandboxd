package safego

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/observability/logging"
)

// TestGoRecoversPanic asserts a panicking background fn is recovered rather than
// crashing the process. If the recover were missing, the panic would escape the
// goroutine and take down the test binary.
func TestGoRecoversPanic(t *testing.T) {
	logger := logging.New(zap.NewNop())

	var wg sync.WaitGroup
	wg.Add(1)
	Go(context.Background(), logger, func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait()
}

// TestGoRunsFn asserts the happy path actually executes fn.
func TestGoRunsFn(t *testing.T) {
	logger := logging.New(zap.NewNop())

	done := make(chan struct{})
	Go(context.Background(), logger, func() {
		close(done)
	})
	<-done
}

func TestRunRecoversPanicAsError(t *testing.T) {
	err := Run(context.Background(), logging.New(zap.NewNop()), func() error { panic("boom") })
	if err == nil || err.Error() != "panic: boom" {
		t.Fatalf("err = %v, want panic error", err)
	}
}

func TestRunReturnsFnError(t *testing.T) {
	want := errors.New("fail")
	if err := Run(context.Background(), nil, func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
