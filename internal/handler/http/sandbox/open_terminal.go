package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
	fiber "github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/bpradana/sandboxd/internal/appctx"
	"github.com/bpradana/sandboxd/internal/consts"
	"github.com/bpradana/sandboxd/internal/presentation"
	sandboxservice "github.com/bpradana/sandboxd/internal/service/sandbox"
)

const (
	// terminalPingPeriod keeps idle terminals alive through proxies and
	// detects dead peers; a peer silent for terminalPongWait is dropped.
	terminalPingPeriod = 25 * time.Second
	terminalPongWait   = 60 * time.Second
	terminalWriteWait  = 10 * time.Second

	terminalMessageResize   = "resize"
	terminalMessageClose    = "close"
	terminalMessageAttached = "attached"
	terminalMessageExit     = "exit"
	terminalMessageError    = "error"
)

// OpenTerminal godoc
// @Summary Open an interactive terminal
// @Description Upgrades to a WebSocket attached to a login shell on a pseudo-terminal inside the sandbox: a new one, or with terminal_id a running one (recent output is replayed). Binary messages carry raw terminal input (client to server) and output (server to client). Text messages carry JSON control messages: the server first sends {"type":"attached","terminal_id":"..."}; the client sends {"type":"resize","cols":N,"rows":N} and {"type":"close"} to end the terminal; the server sends {"type":"exit","exit_code":N} or {"type":"error","error":"..."} before closing. Closing the socket without "close" detaches: the shell keeps running for SANDBOX_TERMINAL_DETACH_TIMEOUT and can be reattached; a newer attachment takes over from an older one. Authenticate with a bearer key or with a ticket from POST /terminal/tickets; browser Origins must be this server or an allowed CORS origin. While attached, the terminal keeps the sandbox from going idle.
// @Tags terminal
// @Security BearerAuth
// @Param id path string true "Sandbox ID."
// @Param ticket query string false "Single-use terminal ticket."
// @Param terminal_id query string false "Reattach to this running terminal."
// @Param cols query int false "Initial columns." minimum(1) maximum(1000)
// @Param rows query int false "Initial rows." minimum(1) maximum(1000)
// @Param shell query string false "Login shell; bash falls back to sh when absent." Enums(sh, bash)
// @Param cwd query string false "Initial working directory."
// @Success 101 {object} presentation.TerminalControlMessage "Switching to the WebSocket protocol."
// @Failure 400 {object} presentation.ErrorResponseDoc "Validation failed."
// @Failure 401 {object} presentation.ErrorResponseDoc "Missing or invalid API key or ticket."
// @Failure 403 {object} presentation.ErrorResponseDoc "Origin not allowed."
// @Failure 404 {object} presentation.ErrorResponseDoc "Sandbox not found."
// @Failure 409 {object} presentation.ErrorResponseDoc "Sandbox is not running."
// @Failure 426 {object} presentation.ErrorResponseDoc "Not a WebSocket upgrade request."
// @Router /api/v1/sandboxes/{id}/terminal [get]
func (h *Handler) OpenTerminal(c fiber.Ctx) error {
	span := startSpan(c, "handler.sandbox.OpenTerminal")
	defer span.End(trace.WithStackTrace(true))

	if !websocket.IsWebSocketUpgrade(c) {
		return serviceError(c, span, terminalEntity, fiber.ErrUpgradeRequired)
	}
	var req presentation.TerminalRequest
	if err := bindQuery(c, &req); err != nil {
		return bindError(c, span, terminalEntity, queryMessage, err)
	}
	id := c.Params(paramID)
	// Fail with a JSON error while the response can still carry one.
	sb, err := h.service.Get(c.Context(), id)
	if err != nil {
		return serviceError(c, span, terminalEntity, err)
	}
	if sb.State != consts.SandboxStateRunning {
		return serviceError(c, span, terminalEntity, appctx.ErrSandboxNotRunning())
	}

	// The socket outlives this request; keep its values, not its lifetime.
	ctx := context.WithoutCancel(c.Context())
	return websocket.New(func(conn *websocket.Conn) {
		h.runTerminal(ctx, conn, id, req)
	})(c)
}

// runTerminal bridges the WebSocket and the sandbox terminal until either side
// ends. All writes go through write: a WebSocket allows one writer at a time.
func (h *Handler) runTerminal(ctx context.Context, conn *websocket.Conn, id string, req presentation.TerminalRequest) {
	defer func() { _ = conn.Close() }()

	var writeMu sync.Mutex
	write := func(messageType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(terminalWriteWait))
		return conn.WriteMessage(messageType, data)
	}

	done := make(chan struct{})
	defer close(done)
	input := make(chan sandboxservice.TerminalInput, 16)
	go readTerminalInput(conn, input, done)
	go pingTerminal(write, done)

	result, err := h.service.Terminal(ctx, id, req, input, func(data []byte) error {
		return write(websocket.BinaryMessage, data)
	}, func(terminalID string) error {
		return writeControl(write, presentation.TerminalControlMessage{Type: terminalMessageAttached, TerminalID: terminalID})
	})
	if err == nil && result == nil {
		// Detached: the client is gone and the terminal keeps running.
		return
	}

	final := presentation.TerminalControlMessage{Type: terminalMessageExit}
	closeCode := websocket.CloseNormalClosure
	if err != nil {
		h.logger.Ctx(ctx).Warn("terminal ended with error", zap.String("sandbox_id", id), zap.Error(err))
		final = presentation.TerminalControlMessage{Type: terminalMessageError, Error: "internal error"}
		if httpErr, ok := errors.AsType[*appctx.HTTPError](err); ok {
			final.Error = httpErr.Message()
		}
		closeCode = websocket.CloseInternalServerErr
	} else {
		final.ExitCode = &result.ExitCode
		final.Signal = result.Signal
	}
	_ = writeControl(write, final)
	_ = write(websocket.CloseMessage, websocket.FormatCloseMessage(closeCode, final.Type))
}

// readTerminalInput turns client messages into terminal input until the
// socket fails or the terminal ends. Closing input ends the terminal.
func readTerminalInput(conn *websocket.Conn, input chan<- sandboxservice.TerminalInput, done <-chan struct{}) {
	defer close(input)
	extend := func(string) error { return conn.SetReadDeadline(time.Now().Add(terminalPongWait)) }
	_ = extend("")
	conn.SetPongHandler(extend)
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = extend("")

		var next sandboxservice.TerminalInput
		switch messageType {
		case websocket.BinaryMessage:
			next.Data = data
		case websocket.TextMessage:
			var msg presentation.TerminalControlMessage
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			switch {
			case msg.Type == terminalMessageClose:
				next.Close = true
			case msg.Type == terminalMessageResize && msg.Cols > 0 && msg.Rows > 0:
				next.Cols, next.Rows = min(msg.Cols, 1000), min(msg.Rows, 1000)
			default:
				continue
			}
		default:
			continue
		}
		select {
		case input <- next:
		case <-done:
			return
		}
	}
}

func writeControl(write func(int, []byte) error, msg presentation.TerminalControlMessage) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return write(websocket.TextMessage, payload)
}

func pingTerminal(write func(int, []byte) error, done <-chan struct{}) {
	ticker := time.NewTicker(terminalPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if write(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}
}
