import { api, request } from "./api.js";
import { cleanPath, childPath, fileURL, writeMode, sameBytes, decodeText, readBytes, EDIT_LIMIT, DOWNLOAD_LIMIT } from "./file-utils.js";

// The same folder, shown four ways. List is the default because it is the
// only view that carries size and mode for every entry at a glance.
const VIEWS = ["icons", "list", "tree", "columns", "gallery"];
const VIEW_STORAGE = "sandboxd.files.view";
const LIMIT = 1000;

function sortEntries(entries) {
  return [...entries].sort((a, b) => (b.type === "dir") - (a.type === "dir") || a.name.localeCompare(b.name));
}

// Column view puts one pane per path level, so it needs a listing for every
// ancestor of the current directory, not just the directory itself.
function ancestors(path) {
  const levels = ["/"];
  let current = "";
  for (const part of path.split("/").filter(Boolean)) {
    current += `/${part}`;
    levels.push(current);
  }
  return levels;
}

function formatSize(entry) {
  if (entry.type === "dir") return "";
  if (entry.size < 1000) return `${entry.size} bytes`;
  const units = ["kB", "MB", "GB", "TB"];
  let size = entry.size / 1000;
  for (const unit of units) {
    if (size < 1000 || unit === "TB") return `${size < 10 ? size.toFixed(1) : Math.round(size)} ${unit}`;
    size /= 1000;
  }
  return `${entry.size} bytes`;
}

// Compact and sortable, so the extra tree columns fit beside the editor.
function formatDate(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (part) => String(part).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function kindOf(entry) {
  if (entry.type === "dir") return "Folder";
  if (entry.type === "symlink") return "Symlink";
  return entry.type === "file" ? "File" : entry.type;
}

function savedView() {
  try {
    const view = localStorage.getItem(VIEW_STORAGE);
    return VIEWS.includes(view) ? view : "list";
  } catch {
    return "list";
  }
}

export class FileExplorer {
  constructor({ document: doc = document, toast, confirm = (message) => window.confirm(message) }) {
    this.doc = doc;
    this.$ = (id) => doc.querySelector(`#${id}`);
    this.toast = toast;
    this.confirm = confirm;
    this.dialog = this.$("files-dialog");
    this.version = 0;
    this.entries = [];
    this.busy = false;
    this.dirty = false;
    this.stopped = false;
    this.file = null;
    this.view = savedView();
    this.columns = [];
    this.listings = new Map();
    this.expanded = new Set();
    this.$("files-close").onclick = () => this.close();
    this.dialog.addEventListener("cancel", (event) => { event.preventDefault(); this.close(); });
    this.dialog.addEventListener("close", () => { this.version++; this.abort?.abort(); });
    this.$("files-path-form").onsubmit = (event) => { event.preventDefault(); void this.run((ctx) => this.list(this.$("files-path").value, ctx)); };
    this.$("files-up").onclick = () => void this.run((ctx) => this.list(`${this.directory}/..`, ctx));
    this.$("files-refresh").onclick = () => { this.listings.clear(); this.expanded.clear(); void this.run((ctx) => this.list(this.directory, ctx)); };
    for (const button of this.dialog.querySelectorAll("[data-view]")) button.onclick = () => void this.setView(button.getAttribute("data-view"));
    this.$("files-filter").oninput = () => this.render();
    this.$("files-new-file").onclick = () => this.showCreate("file");
    this.$("files-new-folder").onclick = () => this.showCreate("dir");
    this.$("files-create-cancel").onclick = () => { this.$("files-create-form").hidden = true; };
    this.$("files-create-form").onsubmit = (event) => { event.preventDefault(); void this.run((ctx) => this.create(ctx)); };
    this.$("files-upload").onclick = () => this.$("files-upload-input").click();
    this.$("files-upload-input").onchange = () => {
      const files = [...this.$("files-upload-input").files];
      void this.run((ctx) => this.upload(files, ctx));
      this.$("files-upload-input").value = "";
    };
    this.$("files-editor").oninput = () => { this.dirty = this.$("files-editor").value !== this.originalText; this.buttons(); };
    this.$("files-save").onclick = () => void this.run((ctx) => this.save(ctx));
    this.$("files-download").onclick = () => void this.run((ctx) => this.download(ctx));
    this.$("files-delete").onclick = () => void this.run((ctx) => this.remove(ctx));
    this.dialog.addEventListener("keydown", (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") { event.preventDefault(); if (!this.$("files-save").disabled) this.$("files-save").click(); }
    });
    window.addEventListener("beforeunload", (event) => { if (this.dialog.open && this.dirty) { event.preventDefault(); event.returnValue = ""; } });
  }

  context() { return { id: this.sandboxID, version: this.version, signal: this.abort.signal }; }
  current(ctx) { return ctx.version === this.version && ctx.id === this.sandboxID && this.dialog.open && !this.stopped; }
  discard() { return !this.dirty || this.confirm("Discard unsaved changes to this file?"); }
  status(message) { this.$("files-status").textContent = message; }

  async open(sb) {
    if (!this.discard()) return;
    this.abort?.abort();
    this.abort = new AbortController();
    this.version++;
    this.sandboxID = sb.id;
    this.directory = "/";
    this.stopped = false;
    this.busy = false;
    this.entries = [];
    this.columns = [];
    this.listings.clear();
    this.expanded.clear();
    this.$("files-sandbox").textContent = sb.id;
    this.$("files-create-form").hidden = true;
    this.clearEditor();
    this.render();
    if (!this.dialog.open) this.dialog.showModal();
    return this.run((ctx) => this.list("/", ctx));
  }

  close() { if (this.discard()) { this.dirty = false; this.dialog.close(); } }

  async run(action) {
    if (this.busy || this.stopped || !this.dialog.open) return;
    const ctx = this.context();
    this.busy = true;
    this.buttons();
    this.status("Working…");
    try { await action(ctx); }
    catch (error) { if (this.current(ctx) && error.name !== "AbortError") this.status(error.message); }
    finally { if (this.current(ctx)) { this.busy = false; this.buttons(); } }
  }

  buttons() {
    const disabled = this.busy || this.stopped;
    for (const node of this.dialog.querySelectorAll("[data-file-action]")) node.disabled = disabled;
    this.$("files-up").disabled = disabled || this.directory === "/";
    this.$("files-save").disabled = disabled || !this.editable || !this.dirty;
    this.$("files-download").disabled = disabled || !this.file || !["file", "symlink"].includes(this.file.type);
    this.$("files-delete").disabled = disabled || !this.file;
    this.$("files-editor").readOnly = disabled || !this.editable;
    this.$("files-dirty").textContent = this.dirty ? "Unsaved changes" : "";
    this.dialog.setAttribute("aria-busy", String(this.busy));
  }

  clearEditor() {
    this.file = null;
    this.editable = false;
    this.dirty = false;
    this.originalText = "";
    this.originalBytes = null;
    this.$("files-editor").value = "";
    this.$("files-editor-name").textContent = "Select a file";
    this.$("files-editor-info").textContent = "Edit UTF-8 text up to 1 MiB, or download any regular file up to 256 MiB.";
  }

  async list(path, ctx) {
    path = cleanPath(path);
    const entries = await api("GET", fileURL(ctx.id, path, "list"), undefined, { signal: ctx.signal });
    if (!this.current(ctx)) return;
    if (this.directory !== path) this.$("files-filter").value = "";
    this.directory = path;
    this.entries = entries || [];
    this.listings.set(path, this.entries);
    this.$("files-path").value = path;
    if (this.view === "columns" && !(await this.syncColumns(ctx))) return;
    this.render();
    this.status(`${this.entries.length} items`);
  }

  visible() {
    const query = this.$("files-filter").value.toLocaleLowerCase();
    return sortEntries(this.entries.filter((entry) => entry.name.toLocaleLowerCase().includes(query)));
  }

  setView(view) {
    if (!VIEWS.includes(view) || view === this.view) return;
    this.view = view;
    try { localStorage.setItem(VIEW_STORAGE, view); } catch { /* Storage unavailable: the view simply resets on reload. */ }
    this.render();
    // Column view is the only one that needs listings this call does not have.
    if (view === "columns") return this.run(async (ctx) => { if (await this.syncColumns(ctx)) this.render(); });
  }

  render() {
    const entries = this.visible();
    const shown = entries.slice(0, LIMIT);
    this.dialog.setAttribute("data-view", this.view);
    for (const view of VIEWS) {
      this.$(`files-view-${view}`).setAttribute("aria-pressed", String(view === this.view));
      this.$(`files-pane-${view}`).hidden = view !== this.view;
    }
    if (this.view === "icons") this.renderTiles(this.$("files-pane-icons"), shown);
    else if (this.view === "tree") this.renderTree();
    else if (this.view === "columns") this.renderColumns();
    else if (this.view === "gallery") this.renderGallery(shown);
    else this.renderList(shown);
    this.$("files-empty").hidden = entries.length > 0;
    this.$("files-count").textContent = entries.length > LIMIT ? `Showing ${LIMIT} of ${entries.length}; filter to narrow the list.` : `${entries.length} items`;
    this.buttons();
  }

  renderList(entries) {
    this.$("files-list").replaceChildren();
    for (const entry of entries) {
      const row = this.doc.createElement("tr");
      const name = this.doc.createElement("td");
      const button = this.doc.createElement("button");
      button.className = "file-name";
      button.type = "button";
      button.setAttribute("data-file-action", "");
      button.textContent = `${entry.type === "dir" ? "▸ " : entry.type === "symlink" ? "↗ " : ""}${entry.name}`;
      button.title = entry.link_target ? `Link to ${entry.link_target}` : entry.path;
      button.onclick = () => void this.run((ctx) => this.select(entry, ctx));
      name.append(button);
      row.append(name);
      for (const value of [entry.type === "dir" ? "Folder" : `${entry.size.toLocaleString()} B`, entry.mode]) {
        const cell = this.doc.createElement("td"); cell.textContent = value; row.append(cell);
      }
      const actions = this.doc.createElement("td");
      const remove = this.doc.createElement("button");
      remove.type = "button";
      remove.className = "btn small danger";
      remove.textContent = "Delete";
      remove.setAttribute("aria-label", `Delete ${entry.name}`);
      remove.setAttribute("data-file-action", "");
      remove.onclick = () => void this.run((ctx) => this.remove(ctx, entry));
      actions.append(remove);
      row.append(actions);
      this.$("files-list").append(row);
    }
  }

  // One row per entry, with the children of an opened folder right below it.
  renderTree() {
    const body = this.$("files-tree-list");
    const rows = [];
    const walk = (entries, depth) => {
      for (const entry of entries) {
        if (rows.length >= LIMIT) return;
        rows.push(this.treeRow(entry, depth));
        if (entry.type !== "dir" || !this.expanded.has(entry.path)) continue;
        const children = this.listings.get(entry.path);
        if (children) walk(sortEntries(children), depth + 1);
      }
    };
    walk(this.visible(), 0);
    body.replaceChildren(...rows);
  }

  treeRow(entry, depth) {
    const open = entry.type === "dir" && this.expanded.has(entry.path);
    const row = this.make("tr");
    const toggle = entry.type === "dir"
      ? this.make("button", {
          class: "files-tree-toggle",
          type: "button",
          "data-file-action": "",
          "aria-expanded": String(open),
          "aria-label": `${open ? "Collapse" : "Expand"} ${entry.name}`,
          onclick: () => void this.run((ctx) => this.toggle(entry, ctx)),
        }, open ? "\u25be" : "\u25b8")
      : this.make("span", { class: "files-tree-toggle", "aria-hidden": "true" });
    const name = this.make("button", {
      class: "files-tree-name",
      type: "button",
      "data-file-action": "",
      "aria-current": this.file?.path === entry.path ? "true" : null,
      // A folder opens in place here; the path field and Up still move the root.
      title: entry.link_target ? `Link to ${entry.link_target}` : entry.path,
      onclick: () => void this.run((ctx) => (entry.type === "dir" ? this.toggle(entry, ctx) : this.select(entry, ctx))),
    }, this.icon(entry), this.make("span", { class: "files-tree-label" }, entry.name));
    row.append(this.make("td", {}, this.make("div", { class: "files-tree-row" },
      this.make("span", { class: "files-tree-indent", style: `width:${depth * 14}px`, "aria-hidden": "true" }), toggle, name)));
    for (const value of [formatDate(entry.modified_at), formatSize(entry), kindOf(entry)]) {
      row.append(this.make("td", {}, value));
    }
    const remove = this.make("button", {
      class: "btn small danger",
      type: "button",
      "data-file-action": "",
      "aria-label": `Delete ${entry.name}`,
      onclick: () => void this.run((ctx) => this.remove(ctx, entry)),
    }, "Delete");
    row.append(this.make("td", {}, remove));
    return row;
  }

  async toggle(entry, ctx) {
    if (this.expanded.has(entry.path)) {
      this.expanded.delete(entry.path);
      this.render();
      return;
    }
    if (!this.listings.has(entry.path)) {
      const entries = await api("GET", fileURL(ctx.id, entry.path, "list"), undefined, { signal: ctx.signal });
      if (!this.current(ctx)) return;
      this.listings.set(entry.path, entries || []);
    }
    this.expanded.add(entry.path);
    this.render();
    this.status(`Opened ${entry.path}`);
  }

  renderTiles(container, entries) {
    container.replaceChildren();
    for (const entry of entries) container.append(this.tile(entry));
  }

  tile(entry) {
    const open = this.make("button", {
      class: "files-tile-open",
      type: "button",
      "data-file-action": "",
      "aria-current": this.file?.path === entry.path ? "true" : null,
      title: entry.link_target ? `Link to ${entry.link_target}` : entry.path,
      onclick: () => void this.run((ctx) => this.select(entry, ctx)),
    }, this.icon(entry, 30), this.make("span", { class: "files-tile-name" }, entry.name));
    const remove = this.make("button", {
      class: "files-tile-delete",
      type: "button",
      "data-file-action": "",
      "aria-label": `Delete ${entry.name}`,
      title: `Delete ${entry.name}`,
      onclick: () => void this.run((ctx) => this.remove(ctx, entry)),
    }, "\u2715");
    return this.make("div", { class: "files-tile" }, open, remove);
  }

  renderColumns() {
    const pane = this.$("files-pane-columns");
    pane.replaceChildren();
    const last = this.columns.length - 1;
    for (const [index, column] of this.columns.entries()) {
      // The filter narrows the folder in view. Ancestors stay whole, so the
      // path you came through never disappears from the panes on the left.
      const entries = index === last ? this.visible().slice(0, LIMIT) : sortEntries(column.entries);
      const list = this.make("ul", { class: "files-column" });
      const followed = this.columns[index + 1]?.path;
      for (const entry of entries) {
        const current = entry.path === followed || (index === last && this.file?.path === entry.path);
        const button = this.make("button", {
          class: "files-column-item",
          type: "button",
          "data-file-action": "",
          "aria-current": current ? "true" : null,
          title: entry.link_target ? `Link to ${entry.link_target}` : entry.path,
          onclick: () => void this.run((ctx) => this.select(entry, ctx)),
        }, this.icon(entry), this.make("span", { class: "files-column-name" }, entry.name),
           entry.type === "dir" ? this.make("span", { class: "files-chevron", "aria-hidden": "true" }, "\u203a") : null);
        list.append(this.make("li", {}, button));
      }
      if (entries.length === 0) list.append(this.make("li", {}, this.make("p", { class: "empty" }, "Empty")));
      pane.append(list);
    }
    // Follow the path: the deepest pane is the one worth looking at.
    pane.scrollLeft = pane.scrollWidth;
  }

  renderGallery(entries) {
    this.renderTiles(this.$("files-gallery-strip"), entries);
    const preview = this.$("files-gallery-preview");
    preview.replaceChildren();
    if (!this.file) {
      preview.append(this.make("p", {}, "Select a file to preview it here."));
      return;
    }
    const file = this.file;
    preview.append(this.icon(file, 56), this.make("h4", {}, file.path));
    preview.append(this.make("p", {}, `${file.type} \u00b7 ${file.size.toLocaleString()} bytes \u00b7 mode ${file.mode}${file.link_target ? ` \u00b7 \u2192 ${file.link_target}` : ""}`));
    // Only UTF-8 text is decoded on open, so that is all there is to show.
    if (this.originalText) preview.append(this.make("pre", {}, this.originalText.split("\n").slice(0, 40).join("\n")));
    else preview.append(this.make("p", {}, "No text preview for this file. Use Download to inspect it."));
  }

  // Rebuild one pane per path level, fetching only the levels not already read.
  async syncColumns(ctx) {
    this.listings.set(this.directory, this.entries);
    const columns = [];
    for (const level of ancestors(this.directory)) {
      let entries = this.listings.get(level);
      if (!entries) {
        entries = await api("GET", fileURL(ctx.id, level, "list"), undefined, { signal: ctx.signal }) || [];
        if (!this.current(ctx)) return false;
        this.listings.set(level, entries);
      }
      columns.push({ path: level, entries });
    }
    this.columns = columns;
    return true;
  }

  make(tag, props = {}, ...children) {
    const node = this.doc.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (key === "class") node.className = value;
      else if (key.startsWith("on")) node[key] = value;
      else if (value !== undefined && value !== null) node.setAttribute(key, value);
    }
    node.append(...children.flat().filter((child) => child !== null && child !== undefined));
    return node;
  }

  icon(entry, size = 16) {
    if (entry.type === "dir") return this.svg(["M1.6 4.3a1 1 0 0 1 1-1h3.1l1.3 1.6h6.4a1 1 0 0 1 1 1v6.8a1 1 0 0 1-1 1H2.6a1 1 0 0 1-1-1z"], size, "files-icon files-icon-dir");
    const paper = ["M3.6 1.9h4.8l3.8 3.8v8.4H3.6z", "M8.4 1.9v3.9h3.9"];
    if (entry.type === "symlink") return this.svg([...paper, "M6.2 11.3 9.4 8.1", "M7.2 8.1h2.4v2.4"], size, "files-icon");
    return this.svg(paper, size, "files-icon");
  }

  svg(paths, size, className) {
    const ns = "http://www.w3.org/2000/svg";
    const root = this.doc.createElementNS(ns, "svg");
    const attrs = { viewBox: "0 0 16 16", width: size, height: size, fill: "none", stroke: "currentColor", "stroke-width": "1.3", "stroke-linejoin": "round", class: className, "aria-hidden": "true" };
    for (const [key, value] of Object.entries(attrs)) root.setAttribute(key, value);
    for (const d of paths) {
      const path = this.doc.createElementNS(ns, "path");
      path.setAttribute("d", d);
      root.append(path);
    }
    return root;
  }

  async select(entry, ctx) {
    if (entry.type === "dir") return this.list(entry.path, ctx);
    if (entry.type === "symlink") {
      try { await this.list(entry.path, ctx); return; }
      catch (error) { if (error.name === "AbortError") throw error; }
    }
    if (!this.discard()) return;
    this.clearEditor();
    this.file = { ...entry };
    this.$("files-editor-name").textContent = entry.path;
    this.$("files-editor-info").textContent = `${entry.type} · ${entry.size.toLocaleString()} bytes · mode ${entry.mode}${entry.link_target ? ` · → ${entry.link_target}` : ""}`;
    // Views other than the list highlight the open file, so redraw on select.
    this.render();
    if (!["file", "symlink"].includes(entry.type)) { this.status("Special files cannot be opened."); return; }
    const response = await request("GET", fileURL(ctx.id, entry.path), { signal: ctx.signal });
    const bytes = await readBytes(response, EDIT_LIMIT);
    if (!this.current(ctx)) return;
    const decoded = decodeText(bytes);
    this.originalBytes = bytes;
    this.originalText = decoded.text;
    this.crlf = decoded.crlf;
    this.file.mode = response.headers.get("X-File-Mode") || entry.mode;
    this.$("files-editor").value = decoded.text;
    this.editable = entry.type === "file";
    this.render();
    this.status(this.editable ? "Ready to edit" : "Symbolic link opened read-only. Navigate to its target path to edit.");
  }

  showCreate(kind) {
    this.createKind = kind;
    this.$("files-create-label").textContent = kind === "dir" ? "New folder" : "New file";
    this.$("files-create-name").value = "";
    this.$("files-create-form").hidden = false;
    this.$("files-create-name").focus();
  }

  async exists(path, ctx) {
    try { return await api("GET", fileURL(ctx.id, path, "stat"), undefined, { signal: ctx.signal }); }
    catch (error) { if (error.status === 404 && error.code === "path_not_found") return null; throw error; }
  }

  async create(ctx) {
    const path = childPath(this.directory, this.$("files-create-name").value);
    if (await this.exists(path, ctx)) throw new Error("A file or folder with that name already exists.");
    if (!this.current(ctx)) return;
    if (this.createKind === "dir") await api("POST", `/sandboxes/${encodeURIComponent(ctx.id)}/files/mkdir`, { path, mode: "0755" }, { signal: ctx.signal });
    else await request("PUT", fileURL(ctx.id, path, "", { mode: "0644" }), { body: new Uint8Array(), contentType: "application/octet-stream", signal: ctx.signal });
    if (!this.current(ctx)) return;
    this.$("files-create-form").hidden = true;
    this.$("files-filter").value = "";
    await this.list(this.directory, ctx);
    this.status(`Created ${path}`);
  }

  async save(ctx) {
    if (!this.editable || !this.dirty || !this.file) return;
    const file = { ...this.file };
    const text = this.$("files-editor").value;
    const bytes = new TextEncoder().encode(this.crlf ? text.replaceAll("\n", "\r\n") : text);
    if (bytes.length > EDIT_LIMIT) throw new Error("Edited text exceeds 1 MiB.");
    const info = await this.exists(file.path, ctx);
    if (!this.current(ctx)) return;
    if (!info || info.type !== "file") throw new Error("This path is no longer a regular file. Refresh before saving.");
    const response = await request("GET", fileURL(ctx.id, file.path), { signal: ctx.signal });
    const current = await readBytes(response, EDIT_LIMIT);
    if (!this.current(ctx)) return;
    if (!sameBytes(current, this.originalBytes) && !this.confirm("This file changed in the sandbox since you opened it. Overwrite those changes?")) { this.status("Save canceled; sandbox changes were kept."); return; }
    const mode = writeMode(response.headers.get("X-File-Mode") || info.mode || file.mode);
    await request("PUT", fileURL(ctx.id, file.path, "", { mode }), { body: bytes, contentType: "application/octet-stream", signal: ctx.signal });
    if (!this.current(ctx)) return;
    this.originalBytes = bytes;
    this.file.mode = mode;
    this.originalText = text;
    this.dirty = false;
    await this.list(this.directory, ctx);
    this.status(`Saved ${file.path}`);
  }

  async upload(files, ctx) {
    const directory = this.directory;
    let count = 0;
    try {
      for (const file of files) {
        const path = childPath(directory, file.name);
        const existing = await this.exists(path, ctx);
        if (!this.current(ctx)) return;
        if (existing && existing.type !== "file") throw new Error(`${file.name} is not a regular file and will not be overwritten.`);
        if (existing && !this.confirm(`Overwrite ${path}?`)) continue;
        if (this.file?.path === path && this.dirty && !this.discard()) continue;
        this.status(`Uploading ${file.name}…`);
        await request("PUT", fileURL(ctx.id, path, "", { mode: writeMode(existing?.mode) }), { body: file, contentType: "application/octet-stream", signal: ctx.signal });
        if (!this.current(ctx)) return;
        if (this.file?.path === path) this.clearEditor();
        count++;
      }
    } finally {
      if (this.current(ctx)) { this.$("files-filter").value = ""; await this.list(directory, ctx); }
    }
    this.status(`Uploaded ${count} file${count === 1 ? "" : "s"}`);
  }

  async download(ctx) {
    const file = { ...this.file };
    const response = await request("GET", fileURL(ctx.id, file.path), { signal: ctx.signal });
    const bytes = await readBytes(response, DOWNLOAD_LIMIT);
    if (!this.current(ctx)) return;
    const url = URL.createObjectURL(new Blob([bytes]));
    const link = this.doc.createElement("a");
    link.href = url;
    link.download = file.name;
    this.doc.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 60000);
    this.status(`Downloaded ${file.name}`);
  }

  async remove(ctx, entry = this.file) {
    if (!entry) return;
    const file = { ...entry };
    const affectsEditor = this.file && (this.file.path === file.path || this.file.path.startsWith(file.path + "/"));
    if (affectsEditor && !this.discard()) return;
    if (!this.confirm(`Delete ${file.path}${file.type === "dir" ? " and all its contents" : ""}? This cannot be undone.`)) { this.status("Delete canceled."); return; }
    await api("DELETE", fileURL(ctx.id, file.path, "", { recursive: String(file.type === "dir") }), undefined, { signal: ctx.signal });
    if (!this.current(ctx)) return;
    if (affectsEditor) this.clearEditor();
    await this.list(this.directory, ctx);
    this.status(`Deleted ${file.path}`);
  }

  update(sandboxes) {
    if (!this.dialog.open || sandboxes.some((sb) => sb.id === this.sandboxID && sb.state === "running")) return;
    this.abort?.abort();
    this.version++;
    this.stopped = true;
    this.busy = false;
    this.buttons();
    this.status("This sandbox has stopped. Restore a snapshot or create another sandbox to access files.");
  }
}
