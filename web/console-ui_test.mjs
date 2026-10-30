import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { parseHTML } from "linkedom";
import { ConsoleUI, describeImage } from "./dist/console-ui.js";

const html = fs.readFileSync(new URL("./dist/index.html", import.meta.url), "utf8");

function fixture(t) {
  const { window, document } = parseHTML(html);
  const previous = { window: globalThis.window, document: globalThis.document, localStorage: globalThis.localStorage };
  const store = new Map();
  Object.assign(globalThis, { window, document, localStorage: { getItem: (key) => store.get(key), setItem: (key, value) => store.set(key, value) } });
  t.after(() => Object.assign(globalThis, previous));
  const $ = (id) => document.getElementById(id);
  const dialog = $("create-dialog");
  Object.defineProperty(dialog, "open", { get() { return this.hasAttribute("open"); } });
  dialog.showModal = () => dialog.setAttribute("open", "");
  dialog.close = () => dialog.removeAttribute("open");
  const calls = [];
  const ui = new ConsoleUI({ document, onFiles: (sb) => calls.push(["files", sb.id]), onRun: (id) => calls.push(["run", id]), onTerminal: (id) => calls.push(["terminal", id]) });
  const sandboxes = [{ id: "sbx-a-full-id", image: "debian", state: "running", labels: { name: "My project" } }, { id: "sbx-b", image: "alpine", state: "stopped" }];
  const snapshots = [{ id: "snap-one", sandbox_id: "sbx-a-full-id", status: "ready" }];
  for (const sb of sandboxes) {
    const row = document.createElement("li"); row.dataset.id = sb.id; row.textContent = "Short visible ID"; $("sandbox-list").append(row);
  }
  $("snapshot-list").append(document.createElement("li"));
  const data = { sandboxes, snapshots, activeTab: null, snapshotsEnabled: false };
  ui.update(data);
  const key = (target, value) => {
    const event = new window.Event("keydown", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "key", { value }); target.dispatchEvent(event); return event;
  };
  return { $, ui, window, document, calls, store, data, key };
}

test("resource navigation searches full IDs, images and labels and survives refreshes", (t) => {
  const f = fixture(t);
  f.$("resource-search").value = "My project";
  f.$("resource-search").oninput();
  assert.equal(f.$("sandbox-list").children[0].hidden, false);
  assert.equal(f.$("sandbox-list").children[1].hidden, true);
  f.ui.update(f.data);
  assert.equal(f.$("sandbox-list").children[1].hidden, true);
  f.$("resource-search").value = "missing";
  f.$("resource-search").oninput();
  assert.equal(f.$("resource-no-results").hidden, false);
  f.$("nav-snapshots").click();
  assert.equal(f.$("sandboxes-panel").hidden, true);
  assert.equal(f.$("nav-snapshots").getAttribute("aria-pressed"), "true");
  assert.equal(f.$("snapshots-disconnected").hidden, false);
  f.$("snapshots-panel").hidden = false; f.ui.update(f.data);
  assert.equal(f.$("snapshots-disconnected").hidden, true);
  f.$("resource-search").value = "sbx-a-full-id"; f.$("resource-search").oninput();
  assert.equal(f.$("snapshot-list").children[0].hidden, false);
  assert.equal(f.$("nav-sandbox-count").textContent, "2");
});

test("workspace actions follow the selected live sandbox and disable when it stops", (t) => {
  const f = fixture(t);
  assert.equal(f.$("workspace-terminal").disabled, true);
  f.data.activeTab = { kind: "terminal", session: { sandboxID: "sbx-a-full-id", label: "My shell" } };
  f.ui.update(f.data);
  assert.equal(f.$("workspace-name").textContent, "My shell");
  assert.equal(f.$("workspace-kind").textContent, "INTERACTIVE TERMINAL");
  assert.equal(f.$("workspace-image").textContent, "debian");
  assert.equal(f.$("sandbox-list").children[0].classList.contains("selected"), true);
  f.$("workspace-files").click(); f.$("workspace-run").click(); f.$("workspace-terminal").click();
  assert.deepEqual(f.calls, [["files", "sbx-a-full-id"], ["run", "sbx-a-full-id"], ["terminal", "sbx-a-full-id"]]);
  f.data.sandboxes[0].state = "stopped"; f.ui.update(f.data);
  assert.equal(f.$("workspace-files").disabled, true);
  assert.equal(f.$("workspace-run").disabled, true);
  assert.equal(f.$("workspace-summary").textContent, "0 running sandboxes");
});

test("creation and shortcuts respect pending requests, dialogs, and typing", (t) => {
  const f = fixture(t);
  f.key(f.document.body, "n");
  assert.equal(f.$("create-dialog").open, true);
  f.$("create-submit").disabled = true;
  f.$("create-close").click();
  assert.equal(f.$("create-dialog").open, true);
  const cancel = new f.window.Event("cancel", { cancelable: true });
  f.$("create-dialog").dispatchEvent(cancel);
  assert.equal(cancel.defaultPrevented, true);
  f.$("create-submit").disabled = false;
  f.$("create-cancel").click();
  assert.equal(f.$("create-dialog").open, false);
  for (const id of ["resource-search", "files-editor"]) f.key(f.$(id), "n");
  assert.equal(f.$("create-dialog").open, false);
  const terminal = f.document.createElement("div"); terminal.className = "xterm"; f.document.body.append(terminal);
  f.key(terminal, "n");
  assert.equal(f.$("create-dialog").open, false);
  f.$("empty-create").click();
  assert.equal(f.$("create-dialog").open, true);
});

test("theme choice persists and cloud setup opens the correct settings section", (t) => {
  const f = fixture(t);
  assert.equal(f.document.documentElement.dataset.theme, "dark");
  f.$("theme-toggle").click();
  assert.equal(f.document.documentElement.dataset.theme, "light");
  assert.equal(f.store.get("sandboxd.theme"), "light");
  assert.equal(f.$("theme-toggle").getAttribute("aria-label"), "Switch to dark theme");
  const clicks = [];
  f.$("settings-open").onclick = () => clicks.push("settings");
  f.$("cloud-storage-open").onclick = () => clicks.push("cloud");
  f.$("connect-storage").click();
  assert.deepEqual(clicks, ["settings", "cloud"]);
});

test("the complete console starts and routes Files and Run to the selected sandbox", async (t) => {
  const f = fixture(t);
  const keys = ["fetch", "setInterval", "clearInterval", "location", "ResizeObserver", "requestAnimationFrame", "WebSocket"];
  const previous = Object.fromEntries(keys.map((key) => [key, globalThis[key]]));
  t.after(() => Object.assign(globalThis, previous));
  globalThis.setInterval = () => 1;
  globalThis.clearInterval = () => {};
  globalThis.location = { protocol: "http:", host: "console.test", origin: "http://console.test", href: "http://console.test/ui/" };
  globalThis.ResizeObserver = class { observe() {} disconnect() {} };
  globalThis.requestAnimationFrame = (callback) => callback();
  globalThis.WebSocket = class { static OPEN = 1; addEventListener() {} close() {} };
  f.window.Terminal = class {
    constructor() { this.cols = 80; this.rows = 24; }
    loadAddon() {} open() {} onData() {} onBinary() {} onResize() {} focus() {} write() {} dispose() {}
  };
  f.window.FitAddon = { FitAddon: class { fit() {} } };
  const calls = [];
  const json = (data) => new Response(JSON.stringify({ data }), { headers: { "Content-Type": "application/json" } });
  globalThis.fetch = async (url) => {
    const path = new URL(url, "http://console.test").pathname;
    calls.push(path);
    if (path === "/readyz") return json({ status: "ok" });
    if (path.endsWith("/sandboxes")) return json([{ id: "sbx-app", state: "running", image: "debian", network: "none", limits: { cpu_millis: 1000, memory_mb: 512 }, expires_at: new Date(Date.now() + 3600000).toISOString(), created_at: new Date().toISOString() }]);
    if (path.endsWith("/stats")) return json({ memory_bytes: 1024, storage_used_bytes: 2048, storage_limit_bytes: 1048576, cpu_usage_micros: 0, pids_current: 2 });
    if (path.endsWith("/terminal/tickets")) return json({ ticket: "test-ticket" });
    if (path.endsWith("/files/list")) return json([]);
    if (path.endsWith("/images") || path.endsWith("/snapshots")) return json([]);
    throw new Error(`Unexpected mocked request: ${path}`);
  };
  const fileDialog = f.$("files-dialog");
  fileDialog.showModal = () => { fileDialog.open = true; };
  fileDialog.close = () => { fileDialog.open = false; };
  await import(`./dist/app.js?smoke=${Date.now()}`);
  for (let i = 0; i < 5; i++) await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(f.$("sandbox-list").children.length, 1);
  assert.equal(f.$("workspace-summary").textContent, "1 running sandbox");
  f.$("sandbox-list").querySelector(".terminal-action").click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(f.$("workspace-kind").textContent, "INTERACTIVE TERMINAL");
  assert.equal(f.$("workspace-files").disabled, false);
  f.$("workspace-files").click();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(fileDialog.open, true);
  assert.ok(calls.includes("/api/v1/sandboxes/sbx-app/files/list"));
  fileDialog.close();
  f.$("workspace-run").click();
  assert.equal(f.$("tabs").children.length, 2);
  assert.equal(f.$("workspace-kind").textContent, "COMMAND & APP PREVIEW");
  const runTab = f.$("tabs").lastElementChild;
  const back = new f.window.Event("keydown", { bubbles: true, cancelable: true });
  Object.defineProperty(back, "key", { value: "ArrowLeft" });
  runTab.dispatchEvent(back);
  assert.equal(f.$("tabs").firstElementChild.getAttribute("aria-selected"), "true");
  assert.equal(runTab.getAttribute("tabindex"), "-1");
});

test("the image picker says which images are not on this host yet", () => {
  // An image that is here reads as itself: nothing to warn about.
  const local = describeImage({ name: "alpine", local: true }, "arm64");
  assert.equal(local.label, "alpine");
  assert.equal(local.disabled, false);
  assert.equal(local.remote, false);

  // One promoted elsewhere is still usable; it just costs a download once.
  const remote = describeImage({ name: "python-ready", local: false, architecture: "arm64" }, "arm64");
  assert.equal(remote.label, "python-ready — downloads on first use");
  assert.equal(remote.disabled, false);
  assert.equal(remote.remote, true);

  // One promoted on another architecture can never be built here, so it is
  // shown with the reason rather than offered and failing later.
  const foreign = describeImage({ name: "amd-only", local: false, architecture: "amd64" }, "arm64");
  assert.equal(foreign.label, "amd-only — amd64 only");
  assert.equal(foreign.disabled, true);
  assert.equal(foreign.foreign, true);

  // A daemon too old to report its architecture must not disable everything.
  const unknownHost = describeImage({ name: "python-ready", local: false, architecture: "amd64" }, "");
  assert.equal(unknownHost.disabled, false);
  // Neither must an image listed without one.
  assert.equal(describeImage({ name: "legacy", local: false }, "arm64").disabled, false);
});
