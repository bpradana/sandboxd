package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/engine"
	"github.com/bpradana/sandboxd/internal/helper"
	"github.com/bpradana/sandboxd/internal/observability/tracing"
	"github.com/bpradana/sandboxd/internal/presentation"
)

// terminalTicketTTL is how long a terminal ticket can wait to be redeemed.
// Browsers fetch one immediately before opening the WebSocket.
const terminalTicketTTL = 30 * time.Second

// TerminalInput is keyboard input or a window resize for a terminal.
type TerminalInput = engine.TerminalInput

// TerminalOutput receives terminal output; returning an error ends the
// terminal.
type TerminalOutput = engine.TerminalOutputFunc

func (s *service) CreateTerminalTicket(ctx context.Context, id string) (*presentation.TerminalTicketResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.CreateTerminalTicket")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	if _, err := s.runningSandbox(ctx, id); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	ticket, expiresAt := s.tickets.issue(id, s.now())
	return &presentation.TerminalTicketResponse{Ticket: ticket, ExpiresAt: expiresAt}, nil
}

func (s *service) RedeemTerminalTicket(id, ticket string) bool {
	return s.tickets.redeem(id, ticket, s.now())
}

// terminalIDPrefix starts every terminal ID.
const terminalIDPrefix = "term"

// Terminal attaches to a new terminal (a login shell on a pseudo-terminal),
// or with req.TerminalID to a running one, and relays it until the shell
// exits or the caller closes input. Closing input detaches: the shell keeps
// running for SANDBOX_TERMINAL_DETACH_TIMEOUT and the result is nil. attached
// receives the terminal ID before any output. An attached terminal counts as
// activity, so the idle reaper leaves the sandbox alone; the TTL still
// applies.
func (s *service) Terminal(ctx context.Context, id string, req presentation.TerminalRequest, input <-chan TerminalInput, out TerminalOutput, attached func(terminalID string) error) (*presentation.ExecResponse, error) {
	ctx, span := tracing.SpanStart(ctx, "service.sandbox.Terminal")
	defer span.End()
	span.SetAttributes(tracing.String(tracing.SandboxIDKey, id))

	if _, err := s.runningSandbox(ctx, id); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	termReq := engine.TerminalRequest{ID: req.TerminalID, Attach: req.TerminalID != "", User: s.user(req.User), Cwd: req.Cwd, Cols: req.Cols, Rows: req.Rows}
	if !termReq.Attach {
		termReq.ID = helper.NewID(terminalIDPrefix)
		if req.Shell == "sh" {
			termReq.Argv = []string{"/bin/sh", "-l"}
		}
	}
	termReq.Ready = func() error { return attached(termReq.ID) }

	done := s.beginActivity(ctx, id)
	s.metrics.TerminalOpened()
	result, err := s.engine.Terminal(ctx, id, termReq, input, out)
	s.metrics.TerminalClosed()
	done()
	if errors.Is(err, engine.ErrTerminalDetached) {
		return nil, nil
	}
	if err != nil {
		tracing.RecordError(span, err)
		s.metrics.ObserveExec(consts.ExecKindTerminal, consts.ExecOutcomeError, 0)
		// Destroying the sandbox cuts the terminal's connection to init;
		// report that rather than the broken connection.
		if _, stateErr := s.runningSandbox(ctx, id); stateErr != nil {
			return nil, stateErr
		}
		return nil, mapError(err)
	}

	s.observeResult(span, consts.ExecKindTerminal, result)
	return &presentation.ExecResponse{
		ExitCode:   result.ExitCode,
		Signal:     result.Signal,
		DurationMs: result.Duration.Milliseconds(),
	}, nil
}

// ticketStore holds single-use terminal tickets. Tickets are kept as SHA-256
// digests, so lookups do not time-leak the stored values.
type ticketStore struct {
	mu      sync.Mutex
	tickets map[[sha256.Size]byte]ticketEntry
}

type ticketEntry struct {
	sandboxID string
	expiresAt time.Time
}

func newTicketStore() *ticketStore {
	return &ticketStore{tickets: make(map[[sha256.Size]byte]ticketEntry)}
}

func (t *ticketStore) issue(sandboxID string, now time.Time) (string, time.Time) {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	ticket := hex.EncodeToString(raw[:])
	expiresAt := now.Add(terminalTicketTTL)

	t.mu.Lock()
	t.tickets[sha256.Sum256([]byte(ticket))] = ticketEntry{sandboxID: sandboxID, expiresAt: expiresAt}
	t.mu.Unlock()
	return ticket, expiresAt
}

// redeem consumes the ticket. It succeeds once, before expiry, and only for
// the sandbox the ticket was issued for.
func (t *ticketStore) redeem(sandboxID, ticket string, now time.Time) bool {
	key := sha256.Sum256([]byte(ticket))
	t.mu.Lock()
	entry, ok := t.tickets[key]
	if ok {
		delete(t.tickets, key)
	}
	t.mu.Unlock()
	return ok && entry.sandboxID == sandboxID && now.Before(entry.expiresAt)
}

// sweep drops expired tickets that were never redeemed.
func (t *ticketStore) sweep(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, entry := range t.tickets {
		if !now.Before(entry.expiresAt) {
			delete(t.tickets, key)
		}
	}
}
