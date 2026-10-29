// Interactive terminals: xterm.js bridged to the sandbox PTY over WebSocket.
//
// Binary frames carry raw terminal bytes in both directions. Text frames are
// JSON control messages: the server sends {"type":"attached"} first, and
// {"type":"exit"} or {"type":"error"} before closing; the client sends
// {"type":"resize"} and {"type":"close"}. Closing the socket without "close"
// only detaches: the shell keeps running and can be reattached by its ID.

import { api } from "./api.js";

const encoder = new TextEncoder();

// One automatic reattach after an unexpected disconnect (network blip,
// server restart of the HTTP layer); after that the user decides.
const AUTO_REATTACH_DELAY_MS = 1000;

const THEME = {
  background: "#0b0d10",
  foreground: "#e6e8eb",
  cursor: "#5eead4",
  selectionBackground: "#2a3a44",
  black: "#1b1f26",
  red: "#f87171",
  green: "#4ade80",
  yellow: "#fbbf24",
  blue: "#60a5fa",
  magenta: "#c084fc",
  cyan: "#5eead4",
  white: "#d1d5db",
  brightBlack: "#6b7280",
  brightRed: "#fca5a5",
  brightGreen: "#86efac",
  brightYellow: "#fde68a",
  brightBlue: "#93c5fd",
  brightMagenta: "#d8b4fe",
  brightCyan: "#99f6e4",
  brightWhite: "#f9fafb",
};

export class TerminalSession {
  // terminalID reattaches to a terminal that is still running in the sandbox.
  constructor(sandboxID, label, pane, { terminalID = null, onStateChange, onAttached }) {
    this.sandboxID = sandboxID;
    this.label = label;
    this.pane = pane;
    this.terminalID = terminalID;
    this.onStateChange = onStateChange;
    this.onAttached = onAttached;
    this.socket = null;
    this.ended = false;
    // gone: the sandbox stopped, so nothing can be reattached or started.
    this.gone = false;
    this.autoReattached = false;

    const host = document.createElement("div");
    host.className = "term";
    this.bar = document.createElement("div");
    this.bar.className = "pane-bar";
    this.status = document.createElement("span");
    this.action = document.createElement("button");
    this.action.className = "btn small";
    this.action.type = "button";
    this.action.addEventListener("click", () => {
      this.autoReattached = false;
      this.connect();
    });
    this.bar.append(this.status, this.action);
    pane.append(host, this.bar);

    this.term = new window.Terminal({
      cursorBlink: true,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      scrollback: 5000,
      theme: THEME,
    });
    this.fit = new window.FitAddon.FitAddon();
    this.term.loadAddon(this.fit);
    this.term.open(host);

    this.term.onData((data) => this.send(encoder.encode(data)));
    this.term.onBinary((data) => this.send(Uint8Array.from(data, (c) => c.charCodeAt(0))));
    this.term.onResize(({ cols, rows }) => this.send(JSON.stringify({ type: "resize", cols, rows })));

    this.resizeObserver = new ResizeObserver(() => this.refit());
    this.resizeObserver.observe(host);
  }

  refit() {
    // A hidden pane has no size; fitting it would collapse the terminal.
    if (this.pane.offsetParent === null) return;
    try {
      this.fit.fit();
    } catch {
      // Not rendered yet.
    }
  }

  focus() {
    this.refit();
    this.term.focus();
  }

  send(payload) {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(payload);
  }

  // connect reattaches when the terminal is known to be running, otherwise
  // starts a new shell.
  async connect() {
    if (this.gone) return;
    this.setEnded(false);
    this.refit();
    const attaching = Boolean(this.terminalID);
    let ticket;
    try {
      ({ ticket } = await api("POST", `/sandboxes/${encodeURIComponent(this.sandboxID)}/terminal/tickets`));
    } catch (err) {
      if (err.code === "sandbox_not_found" || err.code === "sandbox_not_running") {
        this.markGone();
        return;
      }
      this.term.write(`\r\n\x1b[31m[${err.message}]\x1b[0m\r\n`);
      this.setEnded(true, err.message, attaching ? "Reconnect" : "New shell");
      return;
    }
    if (attaching) {
      // The server replays recent output; start from a clean screen.
      this.term.reset();
    }

    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const query = new URLSearchParams({ ticket, cols: this.term.cols, rows: this.term.rows });
    if (attaching) query.set("terminal_id", this.terminalID);
    const socket = new WebSocket(`${scheme}://${location.host}/api/v1/sandboxes/${encodeURIComponent(this.sandboxID)}/terminal?${query}`);
    socket.binaryType = "arraybuffer";
    this.socket = socket;
    // final is the server's last word: an exit or an error.
    let final = null;

    socket.addEventListener("open", () => this.term.focus());
    socket.addEventListener("message", (event) => {
      if (typeof event.data !== "string") {
        this.term.write(new Uint8Array(event.data));
        return;
      }
      let message;
      try {
        message = JSON.parse(event.data);
      } catch {
        return;
      }
      if (message.type === "attached") {
        this.terminalID = message.terminal_id;
        this.autoReattached = false;
        this.onAttached?.(this);
      } else if (message.type === "exit" || message.type === "error") {
        final = message;
      }
    });
    socket.addEventListener("close", () => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.handleClose(final);
    });
  }

  handleClose(final) {
    if (final?.type === "exit") {
      const how = final.signal ? `killed by ${final.signal}` : `exited with code ${final.exit_code}`;
      this.terminalEnded(`process ${how}`);
      return;
    }
    if (final?.type === "error") {
      if (/not running|not found/.test(final.error) && /sandbox/.test(final.error)) {
        this.markGone();
      } else if (/terminal not found/.test(final.error)) {
        this.terminalEnded("terminal ended while disconnected");
      } else if (/another window/.test(final.error)) {
        this.term.write(`\r\n\x1b[2m[${final.error}]\x1b[0m\r\n`);
        this.setEnded(true, final.error, "Take over");
      } else {
        this.term.write(`\r\n\x1b[31m[${final.error}]\x1b[0m\r\n`);
        this.setEnded(true, final.error, this.terminalID ? "Reconnect" : "New shell");
      }
      return;
    }
    // Dropped without a word: the shell is still running; reattach once.
    if (this.terminalID && !this.autoReattached) {
      this.autoReattached = true;
      this.setEnded(true, "connection lost, reconnecting…", "Reconnect");
      setTimeout(() => {
        if (this.ended && !this.socket && !this.gone && !this.disposed) this.connect();
      }, AUTO_REATTACH_DELAY_MS);
      return;
    }
    this.term.write("\r\n\x1b[2m[connection lost]\x1b[0m\r\n");
    this.setEnded(true, "connection lost; the shell keeps running for a while", this.terminalID ? "Reconnect" : "New shell");
  }

  // terminalEnded: the shell is gone; the next connect starts a new one.
  terminalEnded(text) {
    this.terminalID = null;
    this.term.write(`\r\n\x1b[2m[${text}]\x1b[0m\r\n`);
    this.setEnded(true, text, "New shell");
    this.onAttached?.(this);
  }

  // markGone: the sandbox stopped, so the terminal can never come back.
  markGone() {
    if (this.gone) return;
    this.gone = true;
    this.terminalID = null;
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.term.write("\r\n\x1b[2m[sandbox stopped]\x1b[0m\r\n");
    this.setEnded(true, "sandbox stopped", null);
    this.onAttached?.(this);
  }

  setEnded(ended, text = "", actionLabel = null) {
    this.ended = ended;
    this.pane.classList.toggle("ended", ended);
    this.status.textContent = text;
    this.action.hidden = !actionLabel;
    this.action.textContent = actionLabel || "";
    this.onStateChange?.(this);
    if (!ended) return;
    // The pane bar appears below the terminal; keep the grid in view.
    requestAnimationFrame(() => this.refit());
  }

  // dispose closes the tab. With end, the shell is ended too; without, it
  // is only detached and survives for the server's detach window.
  dispose({ end = true } = {}) {
    this.disposed = true;
    const socket = this.socket;
    this.socket = null;
    if (socket?.readyState === WebSocket.OPEN && end) socket.send(JSON.stringify({ type: "close" }));
    socket?.close();
    this.resizeObserver.disconnect();
    this.term.dispose();
  }
}
