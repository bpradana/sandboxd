package sandbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/presentation"
)

func TestTerminalTicketIsSingleUseAndBound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	f.running(t, "sbx-b")

	resp, err := f.svc.CreateTerminalTicket(context.Background(), "sbx-a")
	if err != nil {
		t.Fatalf("CreateTerminalTicket() error = %v", err)
	}
	if len(resp.Ticket) != 64 || !resp.ExpiresAt.Equal(f.now.Add(terminalTicketTTL)) {
		t.Fatalf("ticket = %+v", resp)
	}
	if f.svc.RedeemTerminalTicket("sbx-b", resp.Ticket) {
		t.Fatal("ticket redeemed for another sandbox")
	}
	// A failed redemption still consumes the ticket.
	if f.svc.RedeemTerminalTicket("sbx-a", resp.Ticket) {
		t.Fatal("ticket redeemed after a failed attempt")
	}

	resp, _ = f.svc.CreateTerminalTicket(context.Background(), "sbx-a")
	if !f.svc.RedeemTerminalTicket("sbx-a", resp.Ticket) {
		t.Fatal("fresh ticket rejected")
	}
	if f.svc.RedeemTerminalTicket("sbx-a", resp.Ticket) {
		t.Fatal("ticket redeemed twice")
	}
}

func TestTerminalTicketExpires(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	expired, _ := f.svc.CreateTerminalTicket(context.Background(), "sbx-a")
	swept, _ := f.svc.CreateTerminalTicket(context.Background(), "sbx-a")
	f.now = f.now.Add(terminalTicketTTL)

	if f.svc.RedeemTerminalTicket("sbx-a", expired.Ticket) {
		t.Fatal("expired ticket redeemed")
	}
	f.svc.tickets.sweep(f.now)
	if len(f.svc.tickets.tickets) != 0 {
		t.Fatalf("sweep left %d tickets", len(f.svc.tickets.tickets))
	}
	if f.svc.RedeemTerminalTicket("sbx-a", swept.Ticket) {
		t.Fatal("swept ticket redeemed")
	}
}

func TestTerminalTicketRequiresRunningSandbox(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	if _, err := f.svc.CreateTerminalTicket(context.Background(), "sbx-missing"); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("CreateTerminalTicket() error = %v", err)
	}
}

func TestTerminalHoldsActivityAndMapsShell(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	input := make(chan TerminalInput)
	f.engine.EXPECT().Terminal(gomock.Any(), "sbx-a", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, req engine.TerminalRequest, _ <-chan engine.TerminalInput, out engine.TerminalOutputFunc) (*engine.ExecResult, error) {
			if len(req.Argv) != 2 || req.Argv[0] != "/bin/sh" || req.Cols != 80 || req.Rows != 24 || req.Attach || !strings.HasPrefix(req.ID, "term-") {
				t.Errorf("request = %+v", req)
			}
			if err := req.Ready(); err != nil {
				t.Errorf("Ready() error = %v", err)
			}
			if !f.svc.busy("sbx-a") {
				t.Error("sandbox not busy while the terminal is attached")
			}
			_ = out([]byte("$ "))
			return &engine.ExecResult{ExitCode: 3, Duration: time.Second}, nil
		})

	var output []byte
	var attachedID string
	resp, err := f.svc.Terminal(context.Background(), "sbx-a", presentation.TerminalRequest{Shell: "sh", Cols: 80, Rows: 24}, input, func(data []byte) error {
		output = append(output, data...)
		return nil
	}, func(terminalID string) error {
		attachedID = terminalID
		return nil
	})
	if err != nil || resp.ExitCode != 3 || string(output) != "$ " || !strings.HasPrefix(attachedID, "term-") {
		t.Fatalf("Terminal() = %+v, %q, %q, %v", resp, output, attachedID, err)
	}
	if f.svc.busy("sbx-a") {
		t.Fatal("sandbox still busy after the terminal ended")
	}
}

func TestTerminalMapsEngineErrors(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	f.engine.EXPECT().Terminal(gomock.Any(), "sbx-a", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, engine.ErrTerminalLimit)

	_, err := f.svc.Terminal(context.Background(), "sbx-a", presentation.TerminalRequest{}, nil, noOutput, noAttach)
	if httpStatus(err) != http.StatusTooManyRequests {
		t.Fatalf("Terminal() error = %v", err)
	}
	if _, err := f.svc.Terminal(context.Background(), "sbx-missing", presentation.TerminalRequest{}, nil, nil, noAttach); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("Terminal() on a missing sandbox error = %v", err)
	}
}

func TestTerminalReportsSandboxStoppedDuringSession(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	f.engine.EXPECT().Terminal(gomock.Any(), "sbx-a", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, string, engine.TerminalRequest, <-chan engine.TerminalInput, engine.TerminalOutputFunc) (*engine.ExecResult, error) {
			_ = f.repo.Delete(context.Background(), "sbx-a")
			return nil, errors.New("sandbox init connection: EOF")
		})

	_, err := f.svc.Terminal(context.Background(), "sbx-a", presentation.TerminalRequest{}, nil, noOutput, noAttach)
	if status := httpStatus(err); status != http.StatusNotFound && status != http.StatusConflict {
		t.Fatalf("Terminal() error = %v", err)
	}
}

func noOutput([]byte) error { return nil }

func noAttach(string) error { return nil }

func TestTerminalReattachAndDetach(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.running(t, "sbx-a")
	f.engine.EXPECT().Terminal(gomock.Any(), "sbx-a", gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, req engine.TerminalRequest, _ <-chan engine.TerminalInput, _ engine.TerminalOutputFunc) (*engine.ExecResult, error) {
			if !req.Attach || req.ID != "term-123" {
				t.Errorf("request = %+v", req)
			}
			return nil, engine.ErrTerminalDetached
		})

	resp, err := f.svc.Terminal(context.Background(), "sbx-a", presentation.TerminalRequest{TerminalID: "term-123"}, nil, noOutput, noAttach)
	if resp != nil || err != nil {
		t.Fatalf("detached Terminal() = %+v, %v; want nil, nil", resp, err)
	}

	f.engine.EXPECT().Terminal(gomock.Any(), "sbx-a", gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, engine.ErrTerminalNotFound)
	if _, err := f.svc.Terminal(context.Background(), "sbx-a", presentation.TerminalRequest{TerminalID: "term-gone"}, nil, noOutput, noAttach); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("Terminal() on an ended terminal error = %v", err)
	}
}
