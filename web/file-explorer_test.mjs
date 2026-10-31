import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { parseHTML } from "linkedom";
import { FileExplorer } from "./dist/file-explorer.js";
import { setAPIKey, request, APIError } from "./dist/api.js";
import { childPath, cleanPath, decodeText, readBytes, fileURL } from "./dist/file-utils.js";

const encode = (text) => new TextEncoder().encode(text);
const html = fs.readFileSync(new URL("./dist/index.html", import.meta.url), "utf8");
const json = (data, status = 200) => new Response(JSON.stringify({ data }), { status, headers: { "Content-Type": "application/json" } });

// Node has no Web Storage, and the console stores the chosen view in it.
function storage(t) {
  if (globalThis.localStorage) return globalThis.localStorage;
  const data = new Map();
  globalThis.localStorage = {
    getItem: (key) => (data.has(key) ? data.get(key) : null),
    setItem: (key, value) => data.set(key, String(value)),
    removeItem: (key) => data.delete(key),
  };
  t.after(() => { delete globalThis.localStorage; });
  return globalThis.localStorage;
}

function fixture(t, { view } = {}) {
  const store = storage(t);
  if (view) store.setItem("sandboxd.files.view", view);
  else store.removeItem("sandboxd.files.view");
  t.after(() => store.removeItem("sandboxd.files.view"));
  const { window, document } = parseHTML(html);
  const previous = { window: globalThis.window, fetch: globalThis.fetch };
  globalThis.window = window;
  t.after(() => { globalThis.window = previous.window; globalThis.fetch = previous.fetch; setAPIKey(""); });
  const dialog = document.querySelector("#files-dialog");
  dialog.open = false;
  dialog.showModal = () => { dialog.open = true; };
  dialog.close = () => { dialog.open = false; dialog.dispatchEvent(new window.Event("close")); };
  const files = new Map([
    ["/", { type: "dir" }], ["/project", { type: "dir" }],
    ["/project/app.py", { type: "file", bytes: encode("print('hello')\n"), mode: "0755" }],
    ["/project/binary", { type: "file", bytes: new Uint8Array([0, 1, 2]) }],
    ["/project/link", { type: "symlink", link_target: "/project/app.py" }],
    ["/project/<img src=x onerror=alert(1)>", { type: "file", bytes: encode("safe") }],
  ]);
  const calls = [], confirmations = [];
  let answer = true;
  const explorer = new FileExplorer({ document, toast() {}, confirm(message) { confirmations.push(message); return answer; } });
  const info = (path, file) => ({ path, name: path.split("/").at(-1), type: file.type, size: file.bytes?.length || 0, mode: file.mode || "0644", modified_at: "2026-10-30T16:00:00Z", link_target: file.link_target });
  const fetch = async (url, options = {}) => {
    const parsed = new URL(url, "http://localhost");
    const path = parsed.searchParams.get("path"), method = options.method || "GET";
    calls.push({ url, method, options, path });
    if (method === "POST" && parsed.pathname.endsWith("/mkdir")) {
      const body = JSON.parse(options.body); files.set(body.path, { type: "dir" }); return json({});
    }
    if (parsed.pathname.endsWith("/changes")) {
      return json({
        entries: [
          { path: "/etc/resolv.conf", kind: "modified", type: "file", size: 20, mode: "0644", modified_at: "2026-10-30T16:00:00Z", seeded: true },
          { path: "/project/app.py", kind: "modified", type: "file", size: 15, mode: "0755", modified_at: "2026-10-30T16:00:00Z", seeded: false },
          { path: "/project/out", kind: "added", type: "dir", size: 0, mode: "0755", modified_at: "2026-10-30T16:00:00Z", seeded: false },
          { path: "/project/gone.txt", kind: "deleted", size: 0, seeded: false },
        ],
        truncated: false,
      });
    }
    const file = files.get(path);
    if (method === "PUT") {
      files.set(path, { type: "file", mode: parsed.searchParams.get("mode"), bytes: new Uint8Array(await new Response(options.body).arrayBuffer()) }); return json({});
    }
    if (!file) return new Response(JSON.stringify({ message: "path not found", error_code: "path_not_found" }), { status: 404 });
    if (method === "DELETE") {
      for (const key of [...files.keys()]) if (key === path || key.startsWith(path + "/")) files.delete(key);
      return json({});
    }
    if (parsed.pathname.endsWith("/stat")) return json(info(path, file));
    if (parsed.pathname.endsWith("/list")) {
      if (file.type !== "dir") return new Response(JSON.stringify({ message: "not directory" }), { status: 409 });
      return json([...files].filter(([key]) => key !== path && cleanPath(key + "/..") === path).map(([key, value]) => info(key, value)));
    }
    const target = file.type === "symlink" ? files.get(file.link_target) : file;
    return new Response(target.bytes, { headers: { "Content-Length": String(target.bytes.length), "X-File-Mode": target.mode || "0644" } });
  };
  globalThis.fetch = fetch;
  const select = async (path) => explorer.run((ctx) => explorer.select(info(path, files.get(path)), ctx));
  const edit = (text) => { const area = document.querySelector("#files-editor"); area.value = text; area.oninput(); };
  return { explorer, document, files, calls, confirmations, fetch, info, select, edit, store, answer(value) { answer = value; } };
}

test("browse folders, filter filenames safely, create files and folders", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.explorer.run((ctx) => f.explorer.list("/project", ctx));
  assert.equal(f.document.querySelectorAll("#files-list tr").length, 4);
  assert.equal(f.document.querySelector("#files-list img"), null);
  assert.match(f.document.querySelector("#files-list").textContent, /<img src=x/);
  f.document.querySelector("#files-filter").value = "app.py";
  f.document.querySelector("#files-filter").oninput();
  assert.equal(f.document.querySelectorAll("#files-list tr").length, 1);
  f.explorer.showCreate("dir");
  f.document.querySelector("#files-create-name").value = "new folder";
  await f.explorer.run((ctx) => f.explorer.create(ctx));
  assert.equal(f.files.get("/project/new folder").type, "dir");
  f.explorer.showCreate("file");
  f.document.querySelector("#files-create-name").value = "new.txt";
  await f.explorer.run((ctx) => f.explorer.create(ctx));
  assert.equal(f.files.get("/project/new.txt").bytes.length, 0);
  f.document.querySelector("#files-create-name").value = "app.py";
  await f.explorer.run((ctx) => f.explorer.create(ctx));
  assert.equal(new TextDecoder().decode(f.files.get("/project/app.py").bytes), "print('hello')\n");
});

test("edit preserves permissions, catches agent changes, and protects unsaved work", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.select("/project/app.py");
  f.edit("print('edited')\n");
  assert.equal(f.document.querySelector("#files-save").disabled, false);
  await f.explorer.run((ctx) => f.explorer.save(ctx));
  assert.equal(f.files.get("/project/app.py").mode, "0755");
  assert.equal(new TextDecoder().decode(f.files.get("/project/app.py").bytes), "print('edited')\n");
  f.edit("my changes\n");
  f.files.get("/project/app.py").bytes = encode("agent changes\n");
  f.answer(false);
  await f.explorer.run((ctx) => f.explorer.save(ctx));
  assert.equal(new TextDecoder().decode(f.files.get("/project/app.py").bytes), "agent changes\n");
  assert.equal(f.explorer.dirty, true);
  assert.ok(f.confirmations.some((message) => message.includes("changed in the sandbox")));
  await f.explorer.close();
  assert.equal(f.document.querySelector("#files-dialog").open, true);
});

test("binary and symbolic link previews are never editable", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.select("/project/binary");
  assert.equal(f.document.querySelector("#files-editor").readOnly, true);
  assert.match(f.document.querySelector("#files-status").textContent, /Binary/);
  assert.equal(f.document.querySelector("#files-download").disabled, false);
  await f.select("/project/link");
  assert.equal(f.document.querySelector("#files-editor").readOnly, true);
  assert.equal(f.document.querySelector("#files-save").disabled, true);
  assert.match(f.document.querySelector("#files-editor").value, /hello/);
});

test("upload and delete require confirmation and preserve another file's unsaved edits", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.explorer.run((ctx) => f.explorer.list("/project", ctx));
  f.answer(false);
  await f.explorer.run((ctx) => f.explorer.upload([new File(["overwrite"], "app.py")], ctx));
  assert.match(new TextDecoder().decode(f.files.get("/project/app.py").bytes), /hello/);
  f.answer(true);
  await f.explorer.run((ctx) => f.explorer.upload([new File([new Uint8Array([1, 2, 3])], "uploaded.bin")], ctx));
  assert.deepEqual(f.files.get("/project/uploaded.bin").bytes, new Uint8Array([1, 2, 3]));
  await f.explorer.run((ctx) => f.explorer.upload([new File(["kept"], "partial.txt"), new File(["no"], "link")], ctx));
  assert.ok(f.explorer.entries.some((entry) => entry.name === "partial.txt"));
  assert.match(f.document.querySelector("#files-status").textContent, /not a regular file/);
  await f.select("/project/app.py"); f.edit("unsaved");
  await f.explorer.run((ctx) => f.explorer.remove(ctx, f.info("/project/uploaded.bin", f.files.get("/project/uploaded.bin"))));
  assert.equal(f.explorer.dirty, true);
  assert.equal(f.document.querySelector("#files-editor").value, "unsaved");
  f.answer(false);
  await f.explorer.run((ctx) => f.explorer.remove(ctx, f.info("/project", f.files.get("/project"))));
  assert.ok(f.files.has("/project"));
  f.answer(true);
  await f.explorer.run((ctx) => f.explorer.remove(ctx, f.info("/project", f.files.get("/project"))));
  assert.equal(f.files.has("/project"), false);
  assert.equal(f.files.has("/project/app.py"), false);
});

test("switching sandboxes discards stale reads and never saves to the new target", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-old" });
  await f.select("/project/app.py"); f.edit("edited");
  let release, started;
  const pending = new Promise((resolve) => { release = resolve; });
  const requested = new Promise((resolve) => { started = resolve; });
  globalThis.fetch = async (url, options) => {
    if (url.includes("sandbox-old/files?") && options.method === "GET") { started(); await pending; }
    return f.fetch(url, options);
  };
  const saving = f.explorer.run((ctx) => f.explorer.save(ctx));
  await requested;
  await f.explorer.open({ id: "sandbox-new" });
  release(); await saving;
  assert.equal(f.explorer.sandboxID, "sandbox-new");
  assert.equal(f.calls.filter((call) => call.method === "PUT").length, 0);
});

test("sandbox stop disables operations and authenticated downloads keep keys out of URLs", async (t) => {
  const f = fixture(t);
  setAPIKey("file-explorer-test-token");
  await f.explorer.open({ id: "sandbox-a" });
  const response = await request("GET", fileURL("sandbox-a", "/project/app.py"));
  assert.match(await response.text(), /hello/);
  assert.equal(f.calls.at(-1).options.headers.Authorization, "Bearer file-explorer-test-token");
  assert.ok(!f.calls.at(-1).url.includes("file-explorer-test-token"));
  await assert.rejects(request("GET", fileURL("sandbox-a", "/missing")), (error) => error instanceof APIError && error.status === 404 && error.code === "path_not_found");
  f.explorer.update([]);
  assert.equal(f.document.querySelector("#files-upload").disabled, true);
  assert.match(f.document.querySelector("#files-status").textContent, /stopped/);
});

test("stopping the sandbox during an overwrite decision never writes stale edits", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.select("/project/app.py");
  f.edit("my edits\n");
  f.files.get("/project/app.py").bytes = encode("agent edits\n");
  let resolve, requested;
  const waiting = new Promise((done) => { requested = done; });
  f.explorer.confirm = () => { requested(); return new Promise((done) => { resolve = done; }); };
  const saving = f.explorer.run((ctx) => f.explorer.save(ctx));
  await waiting;
  f.explorer.update([]);
  resolve(true);
  await saving;
  assert.equal(f.calls.filter((call) => call.method === "PUT").length, 0);
  assert.equal(new TextDecoder().decode(f.files.get("/project/app.py").bytes), "agent edits\n");
});

test("paths, UTF-8 files, BOM and streamed size limits", async () => {
  assert.equal(cleanPath("/a/../../b//./"), "/b");
  assert.equal(childPath("/project", "a & b.txt"), "/project/a & b.txt");
  assert.throws(() => childPath("/", "../escape"), /slashes/);
  assert.throws(() => cleanPath("relative"), /absolute/);
  const parsed = decodeText(encode("\uFEFFhello\r\n"));
  assert.equal(parsed.text, "\uFEFFhello\n"); assert.equal(parsed.crlf, true);
  assert.throws(() => decodeText(new Uint8Array([255])), /UTF-8/);
  let canceled = false;
  const response = new Response(new ReadableStream({ start(controller) { controller.enqueue(new Uint8Array(4)); }, cancel() { canceled = true; } }));
  await assert.rejects(readBytes(response, 3), /limit/);
  assert.equal(canceled, true);
});

test("every view shows the same folder, highlights the open file, and is remembered", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  await f.explorer.run((ctx) => f.explorer.list("/project", ctx));
  const pane = (name) => f.document.querySelector(`#files-pane-${name}`);
  const pressed = (name) => f.document.querySelector(`#files-view-${name}`).getAttribute("aria-pressed");
  assert.equal(pane("list").hidden, false);
  assert.equal(pressed("list"), "true");

  f.explorer.setView("icons");
  assert.equal(pane("list").hidden, true);
  assert.equal(pressed("icons"), "true");
  assert.equal(pane("icons").querySelectorAll(".files-tile").length, 4);
  // Names are text in every view, never markup.
  assert.equal(pane("icons").querySelector("img"), null);
  assert.match(pane("icons").textContent, /<img src=x/);
  assert.ok([...pane("icons").querySelectorAll(".files-tile-delete")].some((button) => button.getAttribute("aria-label") === "Delete app.py"));

  // Column view reads the ancestors it has not listed yet, and marks the
  // folder the next pane came from.
  await f.explorer.setView("columns");
  const columns = pane("columns").querySelectorAll(".files-column");
  assert.equal(columns.length, 2);
  assert.equal(columns[0].querySelector(".files-column-item").getAttribute("aria-current"), "true");
  assert.equal(columns[1].querySelectorAll(".files-column-item").length, 4);

  f.explorer.setView("gallery");
  assert.equal(pane("gallery").querySelectorAll(".files-tile").length, 4);
  assert.match(f.document.querySelector("#files-gallery-preview").textContent, /Select a file/);
  await f.select("/project/app.py");
  assert.match(f.document.querySelector("#files-gallery-preview pre").textContent, /print\('hello'\)/);
  assert.equal(pane("gallery").querySelector(".files-tile-open[aria-current='true']").textContent, "app.py");

  // The filter narrows the folder in view, in whichever view that is.
  f.document.querySelector("#files-filter").value = "app.py";
  f.document.querySelector("#files-filter").oninput();
  assert.equal(pane("gallery").querySelectorAll(".files-tile").length, 1);

  assert.equal(f.store.getItem("sandboxd.files.view"), "gallery");
  const next = fixture(t, { view: "columns" });
  assert.equal(next.explorer.view, "columns");
});

test("the tree opens folders in place and names kind, size and date", async (t) => {
  const f = fixture(t, { view: "tree" });
  await f.explorer.open({ id: "sandbox-a" });
  const rows = () => [...f.document.querySelectorAll("#files-tree-list tr")];
  const project = () => f.info("/project", f.files.get("/project"));
  assert.equal(f.explorer.view, "tree");
  assert.equal(rows().length, 1);
  assert.equal(rows()[0].querySelectorAll("td")[3].textContent, "Folder");
  assert.equal(rows()[0].querySelector(".files-tree-toggle").getAttribute("aria-expanded"), "false");

  await f.explorer.run((ctx) => f.explorer.toggle(project(), ctx));
  assert.equal(rows().length, 5);
  assert.equal(rows()[0].querySelector(".files-tree-toggle").getAttribute("aria-expanded"), "true");
  assert.match(rows()[1].querySelector(".files-tree-indent").getAttribute("style"), /14px/);
  const child = rows().find((row) => row.textContent.includes("app.py")).querySelectorAll("td");
  assert.match(child[1].textContent, /2026/);
  assert.equal(child[2].textContent, "15 bytes");
  assert.equal(child[3].textContent, "File");
  assert.equal(rows().find((row) => row.textContent.includes("link")).querySelectorAll("td")[3].textContent, "Symlink");

  // Collapsing takes the children away without another request.
  const reads = f.calls.length;
  await f.explorer.run((ctx) => f.explorer.toggle(project(), ctx));
  assert.equal(rows().length, 1);
  await f.explorer.run((ctx) => f.explorer.toggle(project(), ctx));
  assert.equal(f.calls.length, reads);
  assert.equal(rows().length, 5);
});

test("the change list names what the workload did and hides the daemon's own files", async (t) => {
  const f = fixture(t);
  await f.explorer.open({ id: "sandbox-a" });
  const rows = () => [...f.document.querySelectorAll("#files-changes-list tr")];
  const note = () => f.document.querySelector("#files-changes-note").textContent;

  await f.explorer.toggleChanges();
  assert.equal(f.document.querySelector("#files-pane-changes").hidden, false);
  assert.equal(f.document.querySelector("#files-pane-list").hidden, true);
  // A folder view makes no sense for a whole-sandbox list.
  assert.equal(f.document.querySelector(".view-switch").hidden, true);
  assert.equal(f.document.querySelector("#files-changes").textContent, "Folder");

  assert.equal(rows().length, 3);
  assert.match(note(), /1 file the daemon writes into every sandbox hidden/);
  const kinds = rows().map((row) => row.querySelectorAll("td")[1].textContent);
  assert.ok(kinds.some((kind) => kind.startsWith("modified")) && kinds.some((kind) => kind.startsWith("deleted")));
  assert.equal(f.document.querySelector("#files-count").textContent, "3 changes");

  // A deleted path has nothing left to open, or to size.
  const deleted = rows().find((row) => row.textContent.includes("gone.txt"));
  assert.equal(deleted.querySelector(".files-change-path").disabled, true);
  assert.equal(deleted.querySelectorAll("td")[2].textContent, "");

  f.document.querySelector("#files-changes-seeded").checked = true;
  f.document.querySelector("#files-changes-seeded").onchange();
  assert.equal(rows().length, 4);
  assert.ok(rows().some((row) => row.textContent.includes("/etc/resolv.conf")));

  // A changed file opens in the editor from here.
  const edited = rows().find((row) => row.textContent.includes("app.py"));
  edited.querySelector(".files-change-path").onclick();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.match(f.document.querySelector("#files-editor").value, /hello/);

  await f.explorer.toggleChanges();
  assert.equal(f.document.querySelector("#files-pane-changes").hidden, true);
  assert.equal(f.document.querySelector(".view-switch").hidden, false);
});
