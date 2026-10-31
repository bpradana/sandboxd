// Run tabs: a command on a pseudo-terminal next to the app it serves.
//
// The command's log is an ordinary terminal, so Ctrl-C, colours and progress
// bars all behave. While it runs, the tab polls the sandbox for listening TCP
// ports and opens a capability-scoped preview on the first one it can reach,
// so a development server appears without anyone typing its port.

import { api } from "./api.js";
import { el } from "./dom.js";
import { TerminalSession } from "./terminal.js";

// How often a visible run tab asks the sandbox what is listening. Ports
// appear seconds after a server boots, so this is the latency the user sees.
const PORT_POLL_MS = 2000;

// Ctrl-C, and how long a command may take to act on it before the terminal
// is ended instead, which kills its process group.
const INTERRUPT = new Uint8Array([3]);
const STOP_GRACE_MS = 3000;

// Fractions of the tab the log may take; the rest belongs to the app.
const MIN_SPLIT = 0.15;
const MAX_SPLIT = 0.85;

export class RunSession {
  // terminalID reattaches to a command still running from an earlier page
  // load; port reopens the preview that tab had open.
  constructor(sandboxID, label, pane, { terminalID = null, command = "", port = null, onStateChange, onSave }) {
    this.sandboxID = sandboxID;
    this.label = label;
    this.pane = pane;
    this.onStateChange = onStateChange;
    this.onSave = onSave;
    this.ports = [];
    this.preview = null;
    this.previewPort = port;
    this.previewRevision = 0;
    this.started = Boolean(terminalID);
    this.pendingRestart = false;
    this.stopTimer = null;
    this.gone = false;
    this.disposed = false;
    // Set while a run has not yet shown an app, so the first reachable port
    // opens by itself. Any manual choice hands control back to the user.
    this.autoPreview = !port;

    this.build(command, port);
    this.terminal = new TerminalSession(sandboxID, label, this.log, {
      terminalID,
      command,
      startLabel: "Restart",
      onStateChange: (session) => this.terminalChanged(session),
      onAttached: () => this.onSave?.(),
    });
    if (terminalID) this.terminal.connect();
    if (port) void this.openPreview(port, { automatic: true });
    this.poller = setInterval(() => this.pollPorts(), PORT_POLL_MS);
    this.render();
  }

  // ---------- view ----------

  build(command, port) {
    this.commandInput = el("input", {
      class: "run-command", type: "text", spellcheck: "false", autocomplete: "off",
      placeholder: "npm run dev", "aria-label": "Command to run in the sandbox",
    });
    this.commandInput.value = command;
    this.commandInput.addEventListener("input", () => this.render());
    this.startButton = el("button", { class: "btn small primary", type: "button", onclick: () => this.toggle() }, "Run");
    this.state = el("span", { class: "run-state muted", role: "status" });
    // Submitting restarts with whatever the command now says, so editing it
    // and pressing Enter does the obvious thing.
    const commandForm = el("form", { class: "run-bar", onsubmit: (event) => { event.preventDefault(); this.restart(); } },
      this.startButton, this.commandInput, this.state);

    this.log = el("section", { class: "run-log", "aria-label": "Command output" });
    this.handle = el("div", {
      class: "run-handle", role: "separator", tabindex: "0",
      "aria-label": "Resize the command output", "aria-orientation": "vertical",
    });
    this.handle.addEventListener("pointerdown", (event) => this.startDrag(event));
    this.handle.addEventListener("keydown", (event) => this.nudgeSplit(event));

    this.portList = el("div", { class: "run-ports" });
    this.portInput = el("input", {
      class: "run-port", type: "number", min: "1", max: "65535",
      placeholder: "Port", "aria-label": "Port to preview",
    });
    if (port) this.portInput.value = String(port);
    this.reloadButton = el("button", { class: "btn small", type: "button", onclick: () => this.reload() }, "Reload");
    this.externalLink = el("a", { class: "btn small", target: "_blank", rel: "noopener noreferrer", hidden: "" }, "New tab ↗");
    this.closeButton = el("button", { class: "btn small", type: "button", hidden: "", onclick: () => this.closePreview({ manual: true }) }, "Close app");
    const portForm = el("form", { class: "run-app-bar", onsubmit: (event) => { event.preventDefault(); this.choosePort(Number(this.portInput.value)); } },
      this.portList, this.portInput,
      el("button", { class: "btn small", type: "submit" }, "Open"),
      this.reloadButton, this.externalLink, this.closeButton);

    this.frame = el("iframe", {
      class: "run-frame", title: `Application running in ${this.sandboxID}`,
      sandbox: "allow-scripts allow-same-origin allow-forms allow-downloads",
      referrerpolicy: "no-referrer", hidden: "",
    });
    this.appStatus = el("p", { class: "run-app-status muted", role: "status" },
      "Run a server, or open a port it already listens on.");
    this.app = el("section", { class: "run-app", "aria-label": "App preview" }, portForm, this.frame, this.appStatus);

    this.split = el("div", { class: "run-split" }, this.log, this.handle, this.app);
    this.pane.append(el("div", { class: "run" }, commandForm, this.split));
  }

  // render keeps every control in step with what is actually running.
  render() {
    const running = this.isRunning();
    this.startButton.textContent = running ? "Stop" : "Run";
    this.startButton.classList.toggle("primary", !running);
    this.startButton.classList.toggle("danger", running);
    this.startButton.disabled = this.gone || (!running && !this.commandInput.value.trim());
    this.commandInput.disabled = this.gone;
    this.portInput.disabled = this.gone;
    this.reloadButton.disabled = !this.preview;
    this.closeButton.hidden = !this.preview;
    this.state.textContent = this.runState();
    this.renderPorts();
    this.onStateChange?.(this);
  }

  clearStopTimer() {
    clearTimeout(this.stopTimer);
    this.stopTimer = null;
  }

  runState() {
    if (this.gone) return "sandbox stopped";
    if (this.pendingRestart) return "restarting…";
    if (this.stopTimer) return "stopping…";
    if (this.isRunning()) return "running";
    return this.started ? this.terminal?.statusText || "not running" : "not started";
  }

  renderPorts() {
    this.portList.replaceChildren(...this.ports.map((entry) => {
      const open = entry.port === this.previewPort && Boolean(this.preview);
      return el("button", {
        class: `run-port-chip${open ? " open" : ""}${entry.previewable ? "" : " unreachable"}`,
        type: "button",
        "aria-pressed": String(open),
        title: entry.previewable
          ? `Listening on ${entry.address}`
          : `Listening on ${entry.address} only, which a preview cannot reach. Bind the server to 0.0.0.0 or 127.0.0.1.`,
        onclick: () => this.choosePort(entry.port),
      }, `:${entry.port}`);
    }));
  }

  setAppStatus(text) {
    this.appStatus.textContent = text;
  }

  // describePreview says what the open preview is pointed at, and warns when
  // the sandbox is not actually serving there -- which is the whole reason a
  // preview shows "App is not reachable" instead of a page.
  describePreview() {
    const parts = [`Port ${this.preview.port}`, `expires ${new Date(this.preview.expires_at).toLocaleTimeString()}`];
    const listener = this.ports.find((entry) => entry.port === this.preview.port);
    if (!this.ports.length) parts.push("nothing is listening in this sandbox yet");
    else if (!listener) parts.push(`nothing is listening on port ${this.preview.port}`);
    else if (!listener.previewable) parts.push(`bound to ${listener.address} only, which a preview cannot reach`);
    return parts.join(" · ");
  }

  // refreshAppStatus keeps the line under the app honest as ports come and
  // go: a server that stops is reported, not left looking healthy.
  refreshAppStatus() {
    if (!this.preview) return;
    this.setAppStatus(this.describePreview());
  }

  focus() {
    this.terminal?.focus();
    void this.pollPorts();
  }

  // focusInput puts the cursor where the tab was opened to be used: in the
  // command box to run something, or in the port box to preview an app that
  // is already serving.
  focusInput(which) {
    (which === "port" ? this.portInput : this.commandInput).focus();
  }

  // ---------- the command ----------

  isRunning() {
    return this.started && Boolean(this.terminal) && !this.terminal.ended && !this.gone;
  }

  toggle() {
    if (this.isRunning()) this.stopCommand();
    else this.start();
  }

  // stopCommand interrupts the command the way Ctrl-C in a terminal would,
  // so a server can shut down cleanly. One that ignores the interrupt has
  // its terminal ended instead, which kills its process group.
  stopCommand() {
    if (!this.isRunning() || this.stopTimer) return;
    this.terminal.send(INTERRUPT);
    this.stopTimer = setTimeout(() => {
      this.stopTimer = null;
      if (this.isRunning()) this.terminal.stop();
    }, STOP_GRACE_MS);
    this.render();
  }

  // restart stops what is running and starts the current command once the
  // old one has actually ended.
  restart() {
    if (!this.isRunning()) {
      this.start();
      return;
    }
    this.pendingRestart = true;
    this.stopCommand();
  }

  start() {
    const command = this.commandInput.value.trim();
    if (!command || this.gone) return;
    this.clearStopTimer();
    this.terminal.command = command;
    this.terminal.terminalID = null;
    this.started = true;
    this.autoPreview = true;
    this.terminal.connect();
    this.onSave?.();
    this.render();
  }

  terminalChanged(session) {
    if (session.ended) this.clearStopTimer();
    if (this.pendingRestart && session.ended) {
      this.pendingRestart = false;
      this.start();
      return;
    }
    this.render();
  }

  // ---------- ports and the preview ----------

  async pollPorts() {
    // An unseen tab costs nothing: its ports are read when it comes back.
    if (this.disposed || this.gone || this.pane.offsetParent === null) return;
    let ports;
    try {
      ports = await api("GET", `/sandboxes/${encodeURIComponent(this.sandboxID)}/ports`);
    } catch (err) {
      if (err.code === "sandbox_not_found" || err.code === "sandbox_not_running") this.markGone();
      return;
    }
    if (this.disposed) return;
    this.ports = ports || [];
    this.renderPorts();
    this.refreshAppStatus();
    if (!this.autoPreview || this.preview) return;
    const reachable = this.ports.find((entry) => entry.previewable);
    if (reachable) void this.openPreview(reachable.port, { automatic: true });
  }

  choosePort(port) {
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      this.setAppStatus("Enter a port between 1 and 65535.");
      return;
    }
    this.autoPreview = false;
    void this.openPreview(port);
  }

  // openPreview replaces whatever is on screen with this port's app.
  async openPreview(port, { automatic = false } = {}) {
    const revision = ++this.previewRevision;
    await this.closePreview();
    if (revision !== this.previewRevision || this.gone || this.disposed) return;
    this.previewPort = port;
    this.portInput.value = String(port);
    this.setAppStatus(`Opening port ${port}…`);
    let session;
    try {
      session = await api("POST", `/sandboxes/${encodeURIComponent(this.sandboxID)}/previews`, { port });
    } catch (err) {
      if (revision !== this.previewRevision) return;
      // Keep watching for ports unless previews are off for good.
      this.autoPreview = automatic && err.code !== "preview_unavailable";
      this.setAppStatus(err.message);
      this.render();
      return;
    }
    if (revision !== this.previewRevision || this.disposed) {
      await revokePreview(this.sandboxID, session);
      return;
    }
    const url = new URL(session.url, location.href);
    if (!["http:", "https:"].includes(url.protocol) || url.origin === location.origin) {
      await revokePreview(this.sandboxID, session);
      this.autoPreview = false;
      this.setAppStatus("A preview must have its own origin. Check the server's PREVIEW_BASE_URL.");
      return;
    }
    this.preview = session;
    this.autoPreview = false;
    this.frame.src = session.url;
    this.frame.hidden = false;
    this.externalLink.href = session.url;
    this.externalLink.hidden = false;
    this.setAppStatus(this.describePreview());
    this.onSave?.();
    this.render();
  }

  // reload reopens an expired preview, and otherwise just refetches the page.
  reload() {
    if (!this.preview) return;
    if (new Date(this.preview.expires_at) <= new Date()) {
      void this.openPreview(this.preview.port);
      return;
    }
    this.frame.src = this.preview.url;
  }

  async closePreview({ manual = false } = {}) {
    const session = this.preview;
    this.preview = null;
    if (manual) {
      this.previewRevision++;
      this.autoPreview = false;
      this.previewPort = null;
      this.setAppStatus("Run a server, or open a port it already listens on.");
    }
    this.frame.src = "about:blank";
    this.frame.hidden = true;
    this.externalLink.hidden = true;
    this.externalLink.removeAttribute("href");
    this.render();
    if (manual) this.onSave?.();
    await revokePreview(this.sandboxID, session);
  }

  // ---------- the split ----------

  startDrag(event) {
    event.preventDefault();
    this.handle.setPointerCapture(event.pointerId);
    const move = (moved) => {
      const box = this.split.getBoundingClientRect();
      const stacked = getComputedStyle(this.split).flexDirection === "column";
      this.setSplit(stacked ? (moved.clientY - box.top) / box.height : (moved.clientX - box.left) / box.width);
    };
    const stop = () => {
      this.handle.removeEventListener("pointermove", move);
      this.handle.removeEventListener("pointerup", stop);
      this.handle.removeEventListener("pointercancel", stop);
    };
    this.handle.addEventListener("pointermove", move);
    this.handle.addEventListener("pointerup", stop);
    this.handle.addEventListener("pointercancel", stop);
  }

  nudgeSplit(event) {
    const step = { ArrowLeft: -0.05, ArrowUp: -0.05, ArrowRight: 0.05, ArrowDown: 0.05 }[event.key];
    if (step === undefined) return;
    event.preventDefault();
    const current = Number(this.split.style.getPropertyValue("--run-split").replace("%", "")) || 50;
    this.setSplit(current / 100 + step);
  }

  setSplit(fraction) {
    if (!Number.isFinite(fraction)) return;
    const clamped = Math.min(MAX_SPLIT, Math.max(MIN_SPLIT, fraction));
    this.split.style.setProperty("--run-split", `${(clamped * 100).toFixed(1)}%`);
  }

  // ---------- lifecycle ----------

  // markGone: the sandbox stopped, so neither the command nor the app can
  // come back. The log stays on screen.
  markGone() {
    if (this.gone) return;
    this.gone = true;
    this.pendingRestart = false;
    this.clearStopTimer();
    this.terminal?.markGone();
    void this.closePreview();
    this.setAppStatus("This sandbox has stopped. Restore it or create another to run an app.");
    this.render();
  }

  dispose({ end = true } = {}) {
    this.disposed = true;
    clearInterval(this.poller);
    this.clearStopTimer();
    this.terminal?.dispose({ end });
    void revokePreview(this.sandboxID, this.preview);
    this.preview = null;
  }

  // saved is what a page reload needs to rebuild this tab.
  saved() {
    return {
      kind: "run",
      sandboxID: this.sandboxID,
      label: this.label,
      terminalID: this.terminal?.terminalID || null,
      command: this.commandInput.value.trim(),
      port: this.preview ? this.preview.port : null,
    };
  }

  // A run tab offers a terminal tab's surface, so the tab bar treats both
  // the same way.
  get ended() {
    return this.gone || !this.isRunning();
  }

  get terminalID() {
    return this.terminal?.terminalID || null;
  }
}

// revokePreview releases a preview session. One that already expired, or
// whose sandbox stopped, is gone anyway, so failure is not worth reporting.
async function revokePreview(sandboxID, session) {
  if (!session) return;
  try {
    await api("DELETE", `/sandboxes/${encodeURIComponent(sandboxID)}/previews`, { id: session.id });
  } catch {
    // Expired, revoked, or the sandbox is gone.
  }
}
