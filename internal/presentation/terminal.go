package presentation

import "time"

// TerminalRequest is the query of the terminal WebSocket upgrade.
type TerminalRequest struct {
	// Ticket authenticates browsers, which cannot set an Authorization header
	// on a WebSocket. Clients that can send the header may omit it.
	Ticket string `query:"ticket" validate:"omitempty,hexadecimal,len=64"`
	// TerminalID reattaches to a running terminal (from its "attached"
	// message) instead of starting a new one; shell and cwd are then ignored.
	TerminalID string `query:"terminal_id" validate:"omitempty,startswith=term-,max=64,printascii" example:"term-4f1c2a9b8e7d6c5b4a3f2e1d"`
	// Cols and Rows are the initial window size.
	Cols uint16 `query:"cols" validate:"omitempty,min=1,max=1000" example:"120"`
	Rows uint16 `query:"rows" validate:"omitempty,min=1,max=1000" example:"40"`
	// Shell is the login shell; bash falls back to sh when absent.
	Shell string `query:"shell" validate:"omitempty,oneof=sh bash" example:"bash"`
	// User runs the shell as root or the sandbox user; defaults to
	// SANDBOX_DEFAULT_USER.
	User string `query:"user" validate:"omitempty,oneof=root sandbox" example:"root"`
	// Cwd is the initial working directory.
	Cwd string `query:"cwd" validate:"omitempty,max=4096" example:"/home/sandbox"`
}

type TerminalTicketResponse struct {
	// Ticket is single-use and valid only for this sandbox's terminal endpoint.
	Ticket    string    `json:"ticket" example:"0000000000000000000000000000000000000000000000000000000000000000"`
	ExpiresAt time.Time `json:"expires_at" example:"2026-10-29T10:00:30Z"`
}

// TerminalControlMessage is a JSON text frame on the terminal WebSocket.
// Clients send "resize" and "close" (end the terminal; merely closing the
// socket detaches). The server sends "attached" with the terminal's ID first,
// and "exit" or "error" before closing. Terminal input and output travel as
// binary frames.
type TerminalControlMessage struct {
	Type       string `json:"type" example:"exit"`
	TerminalID string `json:"terminal_id,omitempty" example:"term-4f1c2a9b8e7d6c5b4a3f2e1d"`
	Cols       uint16 `json:"cols,omitempty"`
	Rows       uint16 `json:"rows,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Error      string `json:"error,omitempty"`
}
