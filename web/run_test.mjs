import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { parseHTML } from "linkedom";
import { RunSession } from "./dist/run.js";
import { setAPIKey } from "./dist/api.js";

const html = fs.readFileSync(new URL("./dist/index.html", import.meta.url), "utf8");
const json = (data, status = 200) => new Response(JSON.stringify({ data }), { status, headers: { "Content-Type": "application/json" } });
const error = (message, code, status) => new Response(JSON.stringify({ message, error_code: code }), { status, headers: { "Content-Type": "application/json" } });
// Several turns: a restart chains a socket close, a ticket request and a
// new socket before it settles.
const settle = async () => { for (let turn = 0; turn < 5; turn++) await new Promise((resolve) => setTimeout(resolve, 0)); };

// A terminal stub: enough of xterm.js and of the browser for a run tab to be
// built and driven without a display or a live socket.
class FakeTerminal {
  constructor() { this.cols = 80; this.rows = 24; this.written = []; }
  loadAddon() {}
  open(host) { this.host = host; }
  onData() {} onBinary() {} onResize() {}
  write(data) { this.written.push(String(data)); }
  reset() { this.written = []; }
  focus() {} dispose() {}
}

function fixture(t) {
  const { window, document } = parseHTML(html);
  const previous = { window: globalThis.window, document: globalThis.document, fetch: globalThis.fetch, location: globalThis.location, WebSocket: globalThis.WebSocket, ResizeObserver: globalThis.ResizeObserver, requestAnimationFrame: globalThis.requestAnimationFrame };
  globalThis.window = window;
  globalThis.document = document;
  globalThis.location = { protocol: "http:", host: "console.test", href: "http://console.test/ui/", origin: "http://console.test" };
  globalThis.ResizeObserver = class { observe() {} disconnect() {} };
  globalThis.requestAnimationFrame = (callback) => setTimeout(callback, 0);
  const sockets = [];
  globalThis.WebSocket = class {
    static OPEN = 1;
    constructor(url) { this.url = url; this.readyState = 1; this.sent = []; this.listeners = {}; sockets.push(this); }
    addEventListener(name, handler) { (this.listeners[name] ||= []).push(handler); }
    send(payload) { this.sent.push(payload); }
    close() { this.readyState = 3; for (const handler of this.listeners.close || []) handler(); }
    emit(name, event) { for (const handler of this.listeners[name] || []) handler(event); }
  };
  window.Terminal = FakeTerminal;
  window.FitAddon = { FitAddon: class { fit() {} } };
  t.after(() => { Object.assign(globalThis, previous); setAPIKey(""); });

  // The sandbox under test: what it is listening on, and whether previews
  // are available at all.
  const sandbox = { ports: [], previews: [], previewError: null, revoked: [] };
  const calls = [];
  globalThis.fetch = async (url, options = {}) => {
    const parsed = new URL(url, "http://console.test");
    const method = options.method || "GET";
    calls.push({ path: parsed.pathname, method, body: options.body ? JSON.parse(options.body) : null });
    if (parsed.pathname.endsWith("/ports")) {
      if (sandbox.stopped) return error("sandbox is not running", "sandbox_not_running", 409);
      return json(sandbox.ports);
    }
    if (parsed.pathname.endsWith("/terminal/tickets")) return json({ ticket: "t".repeat(64) }, 201);
    if (parsed.pathname.endsWith("/previews") && method === "POST") {
      if (sandbox.previewError) return error(sandbox.previewError.message, sandbox.previewError.code, sandbox.previewError.status);
      const port = JSON.parse(options.body).port;
      const session = { id: `pv${sandbox.previews.length}`, port, url: `http://abc${port}.preview.test/`, expires_at: new Date(Date.now() + 3600_000).toISOString() };
      sandbox.previews.push(session);
      return json(session, 201);
    }
    if (parsed.pathname.endsWith("/previews") && method === "DELETE") {
      sandbox.revoked.push(JSON.parse(options.body).id);
      return json({});
    }
    throw new Error(`unexpected request ${method} ${parsed.pathname}`);
  };

  const pane = document.createElement("div");
  document.querySelector("#panes").append(pane);
  const open = (options = {}) => {
    const session = new RunSession("sbx-1", "run sbx-1 #1", pane, options);
    t.after(() => session.dispose());
    return session;
  };
  const $ = (selector) => pane.querySelector(selector);
  return { open, pane, $, sandbox, calls, sockets, document };
}

// attach makes a started command look connected, the way the server does.
function attach(session, sockets) {
  const socket = sockets.at(-1);
  socket.emit("message", { data: JSON.stringify({ type: "attached", terminal_id: "term-1" }) });
  assert.equal(session.isRunning(), true);
  return socket;
}

test("running a command sends it to the terminal and offers to stop it", async (t) => {
  const { open, $, calls, sockets } = fixture(t);
  const session = open();

  assert.equal($(".run-state").textContent, "not started");
  assert.equal($(".run-bar .btn").textContent, "Run");
  assert.equal($(".run-bar .btn").disabled, true, "there is nothing to run yet");

  $(".run-command").value = "npm run dev";
  $(".run-command").dispatchEvent(new globalThis.window.Event("input"));
  assert.equal($(".run-bar .btn").disabled, false);

  $(".run-bar .btn").click();
  await settle();
  assert.ok(calls.some((call) => call.path.endsWith("/terminal/tickets")), "asks for a ticket");
  const socket = sockets.at(-1);
  assert.equal(new URL(socket.url).searchParams.get("command"), "npm run dev", "the command travels on the socket query");

  attach(session, sockets);
  assert.equal($(".run-bar .btn").textContent, "Stop");
  assert.equal($(".run-state").textContent, "running");

  $(".run-bar .btn").click();
  assert.deepEqual([...socket.sent.at(-1)], [3], "stop interrupts the command first, the way Ctrl-C would");
  assert.equal($(".run-state").textContent, "stopping…");
});

test("a command that ignores the interrupt has its terminal ended", async (t) => {
  const { open, $, sockets } = fixture(t);
  const session = open({ command: "npm run dev" });
  session.start();
  await settle();
  const socket = attach(session, sockets);

  session.stopCommand();
  assert.deepEqual([...socket.sent.at(-1)], [3]);
  session.stopCommand();
  assert.equal(socket.sent.length, 1, "a second Stop does not pile on interrupts");

  // Nothing has ended; the grace period runs out and the terminal is closed,
  // which kills the command's process group.
  await new Promise((resolve) => setTimeout(resolve, 3100));
  assert.deepEqual(JSON.parse(socket.sent.at(-1)), { type: "close" });

  socket.emit("message", { data: JSON.stringify({ type: "exit", exit_code: 0 }) });
  socket.close();
  await settle();
  assert.equal($(".run-state").textContent, "process exited with code 0", "the log keeps why it ended");
});

test("a run tab can be opened to run a command or to preview a port", async (t) => {
  const { open, $ } = fixture(t);
  const session = open();
  const focused = [];
  for (const node of [$(".run-command"), $(".run-port")]) node.focus = () => focused.push(node.className);

  session.focusInput("command");
  session.focusInput("port");
  assert.deepEqual(focused, ["run-command", "run-port"]);
  // Preview is the same tab entered from the other end: no command yet.
  assert.equal($(".run-command").value, "");
  assert.equal($(".run-bar .btn").disabled, true);
});

test("a listening port opens a preview by itself, once", async (t) => {
  const { open, $, sandbox, calls } = fixture(t);
  const session = open();

  sandbox.ports = [{ port: 3000, address: "0.0.0.0", previewable: true }];
  await session.pollPorts();
  await settle();

  assert.equal(sandbox.previews.length, 1);
  assert.equal(sandbox.previews[0].port, 3000);
  assert.equal($(".run-frame").hidden, false);
  assert.equal($(".run-frame").getAttribute("src"), "http://abc3000.preview.test/");
  assert.equal($(".run-port-chip").textContent, ":3000");
  assert.equal($(".run-port-chip").getAttribute("aria-pressed"), "true");
  assert.equal($(".run-app-status").textContent.startsWith("Port 3000 ·"), true);

  // A second port appearing must not steal the tab from the open app.
  sandbox.ports.push({ port: 4000, address: "0.0.0.0", previewable: true });
  const before = calls.length;
  await session.pollPorts();
  await settle();
  assert.equal(sandbox.previews.length, 1, "the open preview stays");
  assert.equal(calls.slice(before).filter((call) => call.method === "POST").length, 0);
});

test("opening a port nothing serves says so, instead of leaving a blank page", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();

  // Nothing is listening at all: the proxy would answer "App is not
  // reachable", so the tab says why before the user goes looking.
  session.choosePort(3000);
  await settle();
  assert.match($(".run-app-status").textContent, /nothing is listening in this sandbox yet/);

  // Something is listening, but not on the port being previewed.
  sandbox.ports = [{ port: 8080, address: "0.0.0.0", previewable: true }];
  await session.pollPorts();
  await settle();
  assert.match($(".run-app-status").textContent, /nothing is listening on port 3000/);

  // And once the server arrives, the warning goes away.
  sandbox.ports.push({ port: 3000, address: "0.0.0.0", previewable: true });
  await session.pollPorts();
  await settle();
  assert.match($(".run-app-status").textContent, /^Port 3000 · expires /);
  assert.doesNotMatch($(".run-app-status").textContent, /nothing is listening/);
});

test("a server bound where a preview cannot reach is called out", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();
  sandbox.ports = [{ port: 3000, address: "192.168.1.2", previewable: false }];
  await session.pollPorts();
  await settle();

  session.choosePort(3000);
  await settle();
  assert.match($(".run-app-status").textContent, /bound to 192\.168\.1\.2 only, which a preview cannot reach/);
});

test("a port bound where a preview cannot reach is shown but never opened", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();

  sandbox.ports = [{ port: 8000, address: "192.168.1.2", previewable: false }];
  await session.pollPorts();
  await settle();

  assert.equal(sandbox.previews.length, 0, "nothing is opened for an unreachable port");
  const chip = $(".run-port-chip");
  assert.equal(chip.className.includes("unreachable"), true);
  assert.match(chip.getAttribute("title"), /Bind the server to 0\.0\.0\.0 or 127\.0\.0\.1/);
  assert.equal($(".run-frame").hidden, true);
});

test("choosing another port revokes the previous preview and stops auto-opening", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();

  sandbox.ports = [{ port: 3000, address: "0.0.0.0", previewable: true }];
  await session.pollPorts();
  await settle();
  assert.equal(sandbox.previews.length, 1);

  session.choosePort(5173);
  await settle();
  assert.deepEqual(sandbox.revoked, ["pv0"], "the old session is released");
  assert.equal(sandbox.previews.at(-1).port, 5173);
  assert.equal($(".run-port").value, "5173");
  assert.equal(session.autoPreview, false, "a manual choice turns off auto-opening");

  await session.closePreview({ manual: true });
  assert.deepEqual(sandbox.revoked, ["pv0", "pv1"]);
  assert.equal($(".run-frame").hidden, true);
  assert.equal($(".run-app-status").textContent, "Run a server, or open a port it already listens on.");
});

test("an invalid port is refused before any request is made", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();
  for (const port of [0, 70000, Number.NaN]) {
    session.choosePort(port);
    await settle();
  }
  assert.equal(sandbox.previews.length, 0);
  assert.equal($(".run-app-status").textContent, "Enter a port between 1 and 65535.");
});

test("previews disabled on the server stop the tab from asking again", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();
  sandbox.previewError = { message: "app previews are disabled", code: "preview_unavailable", status: 503 };
  sandbox.ports = [{ port: 3000, address: "0.0.0.0", previewable: true }];

  await session.pollPorts();
  await settle();
  assert.equal($(".run-app-status").textContent, "app previews are disabled");
  assert.equal(session.autoPreview, false);

  await session.pollPorts();
  await settle();
  assert.equal(session.ports.length, 1, "ports are still listed; only the preview is off");
});

test("a preview sharing the console's origin is rejected and released", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open();
  const previous = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    if (String(url).endsWith("/previews") && (options?.method || "GET") === "POST") {
      sandbox.previews.push({ id: "pv-same", port: 3000 });
      return json({ id: "pv-same", port: 3000, url: "http://console.test/ui/", expires_at: new Date().toISOString() }, 201);
    }
    return previous(url, options);
  };
  session.choosePort(3000);
  await settle();
  assert.equal($(".run-frame").hidden, true);
  assert.match($(".run-app-status").textContent, /own origin/);
  assert.deepEqual(sandbox.revoked, ["pv-same"], "a preview we refuse to show is released");
});

test("a stopped sandbox ends the command, closes the app and freezes the controls", async (t) => {
  const { open, $, sandbox } = fixture(t);
  const session = open({ command: "npm run dev" });
  session.start();
  await settle();

  sandbox.ports = [{ port: 3000, address: "0.0.0.0", previewable: true }];
  await session.pollPorts();
  await settle();
  assert.equal(sandbox.previews.length, 1);

  sandbox.stopped = true;
  await session.pollPorts();
  await settle();

  assert.equal(session.gone, true);
  assert.equal($(".run-state").textContent, "sandbox stopped");
  assert.equal($(".run-bar .btn").disabled, true);
  assert.equal($(".run-command").disabled, true);
  assert.deepEqual(sandbox.revoked, ["pv0"]);
  assert.match($(".run-app-status").textContent, /This sandbox has stopped/);
});

test("what a run tab saves is what a reload needs to rebuild it", async (t) => {
  const { open, sandbox, sockets } = fixture(t);
  const saves = [];
  const session = open({ command: "npm run dev", onSave: () => saves.push(session.saved()) });

  session.start();
  await settle();
  attach(session, sockets);
  sandbox.ports = [{ port: 3000, address: "0.0.0.0", previewable: true }];
  await session.pollPorts();
  await settle();

  assert.deepEqual(session.saved(), {
    kind: "run", sandboxID: "sbx-1", label: "run sbx-1 #1",
    terminalID: "term-1", command: "npm run dev", port: 3000,
  });
  assert.ok(saves.length > 0, "the tab asks to be saved as it changes");
});

test("restoring a tab reattaches its command and reopens its port", async (t) => {
  const { open, $, sandbox, sockets, calls } = fixture(t);
  const session = open({ terminalID: "term-1", command: "npm run dev", port: 4321 });
  await settle();

  const socket = sockets.at(-1);
  assert.equal(new URL(socket.url).searchParams.get("terminal_id"), "term-1", "reattaches rather than starting again");
  assert.equal(new URL(socket.url).searchParams.has("command"), false, "a reattached command is not started twice");
  assert.equal(sandbox.previews.at(-1).port, 4321);
  assert.equal($(".run-port").value, "4321");
  assert.equal(calls.filter((call) => call.method === "POST" && call.path.endsWith("/previews")).length, 1);
});

test("editing the command and submitting restarts it with the new one", async (t) => {
  const { open, $, sandbox, sockets } = fixture(t);
  const session = open({ command: "npm run dev" });
  session.start();
  await settle();
  const first = attach(session, sockets);

  $(".run-command").value = "npm start";
  $(".run-bar").dispatchEvent(new globalThis.window.Event("submit"));
  assert.equal(session.pendingRestart, true);
  assert.deepEqual([...first.sent.at(-1)], [3], "the old command is interrupted first");
  assert.equal(sockets.length, 1, "and the new one waits for it to end");

  first.emit("message", { data: JSON.stringify({ type: "exit", exit_code: 0 }) });
  first.close();
  await settle();
  assert.equal(session.pendingRestart, false);
  assert.equal(new URL(sockets.at(-1).url).searchParams.get("command"), "npm start");
  assert.equal(sandbox.previews.length, 0);
});
