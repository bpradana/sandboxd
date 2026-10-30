import { api, request } from "./api.js";
import { cleanPath, childPath, fileURL, writeMode, sameBytes, decodeText, readBytes, EDIT_LIMIT, DOWNLOAD_LIMIT } from "./file-utils.js";

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
    this.$("files-close").onclick = () => this.close();
    this.dialog.addEventListener("cancel", (event) => { event.preventDefault(); this.close(); });
    this.dialog.addEventListener("close", () => { this.version++; this.abort?.abort(); });
    this.$("files-path-form").onsubmit = (event) => { event.preventDefault(); void this.run((ctx) => this.list(this.$("files-path").value, ctx)); };
    this.$("files-up").onclick = () => void this.run((ctx) => this.list(`${this.directory}/..`, ctx));
    this.$("files-refresh").onclick = () => void this.run((ctx) => this.list(this.directory, ctx));
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
    this.$("files-path").value = path;
    this.render();
    this.status(`${this.entries.length} items`);
  }

  render() {
    const query = this.$("files-filter").value.toLocaleLowerCase();
    const entries = this.entries.filter((entry) => entry.name.toLocaleLowerCase().includes(query))
      .sort((a, b) => (b.type === "dir") - (a.type === "dir") || a.name.localeCompare(b.name));
    this.$("files-list").replaceChildren();
    for (const entry of entries.slice(0, 1000)) {
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
    this.$("files-empty").hidden = entries.length > 0;
    this.$("files-count").textContent = entries.length > 1000 ? `Showing 1000 of ${entries.length}; filter to narrow the list.` : `${entries.length} items`;
    this.buttons();
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
