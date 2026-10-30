// Workspace navigation and presentation. All resource data comes from app.js.
export function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", `#i-${name}`);
  svg.append(use);
  return svg;
}

// sandboxMeta is the line under a sandbox's name. The node it lives on is in
// it only when the list spans more than one: on a single daemon every card
// would carry the same name, and behind a router it is the one fact that
// says where the sandbox actually is.
export function sandboxMeta(sandbox, nodeCount) {
  const node = nodeCount > 1 ? sandbox.node : "";
  const network = sandbox.network === "none" ? "Isolated network"
    : sandbox.network === "egress" ? "Internet access" : sandbox.network;
  return [node, network, sandbox.ip_address].filter(Boolean).join(" · ");
}

// describeCapacity turns a daemon's capacity report into one line of status.
// It answers the two questions a person actually has while working -- can I
// still create something, and is this host running out -- and keeps the rest
// (which node, which architecture) for the tooltip.
export function describeCapacity(capacity) {
  if (!capacity) return { text: "", title: "", tight: false };
  const slots = capacity.sandboxes || {};
  const host = capacity.host || {};
  const parts = [];
  if (slots.max) parts.push(`${slots.active || 0} of ${slots.max} sandboxes`);
  if (host.memory_available_mb) parts.push(`${size(host.memory_available_mb)} memory free`);
  if (host.storage_free_mb) parts.push(`${size(host.storage_free_mb)} disk free`);
  // Tight means the next create is at risk, which is worth showing, not just
  // reporting: either the slots are nearly gone or the machine's memory is.
  const slotsLeft = slots.max ? 1 - (slots.active || 0) / slots.max : 1;
  const memoryLeft = host.memory_mb ? (host.memory_available_mb || 0) / host.memory_mb : 1;
  return {
    text: parts.join(" · "),
    title: [capacity.node, capacity.architecture, capacity.ready ? "engine ready" : "engine unavailable"].filter(Boolean).join(" · "),
    tight: slotsLeft <= 0.1 || memoryLeft <= 0.1,
  };
}

function size(megabytes) {
  return megabytes >= 1024 ? `${(megabytes / 1024).toFixed(1)} GB` : `${Math.round(megabytes)} MB`;
}

// describeImage says how a base image should read in the create dialog. An
// image promoted from a snapshot exists cluster-wide but may not be on this
// host yet: it is still usable, it just costs a download the first time.
// One promoted on another architecture cannot be built here at all.
export function describeImage(image, hostArchitecture) {
  const remote = image.local === false;
  const foreign = remote && Boolean(image.architecture) && Boolean(hostArchitecture) && image.architecture !== hostArchitecture;
  if (foreign) {
    return { label: `${image.name} — ${image.architecture} only`, disabled: true, remote, foreign };
  }
  return { label: remote ? `${image.name} — downloads on first use` : image.name, disabled: false, remote, foreign };
}

export class ConsoleUI {
  constructor({ document: doc = document, onFiles, onRun, onTerminal }) {
    this.doc = doc;
    this.$ = (id) => doc.getElementById(id);
    this.view = "sandboxes";
    this.data = { sandboxes: [], snapshots: [], activeTab: null, snapshotsEnabled: false, capacity: null };
    this.createDialog = this.$("create-dialog");
    for (const id of ["create-open", "empty-create", "welcome-create"]) this.$(id).onclick = () => this.showCreate();
    for (const id of ["create-close", "create-cancel"]) this.$(id).onclick = () => this.closeCreate();
    this.createDialog.addEventListener("cancel", (event) => {
      if (this.$("create-submit").disabled) event.preventDefault();
    });
    for (const view of ["sandboxes", "snapshots"]) this.$(`nav-${view}`).onclick = () => this.setView(view);
    this.$("resource-search").oninput = () => this.filter();
    this.$("connect-storage").onclick = () => { this.$("settings-open").click(); this.$("cloud-storage-open").click(); };
    this.$("workspace-files").onclick = () => { const sb = this.activeSandbox(); if (sb) onFiles(sb); };
    this.$("workspace-run").onclick = () => { const sb = this.activeSandbox(); if (sb) onRun(sb.id); };
    this.$("workspace-terminal").onclick = () => { const sb = this.activeSandbox(); if (sb) onTerminal(sb.id); };
    let saved;
    try { saved = localStorage.getItem("sandboxd.theme"); } catch { /* Private browsing. */ }
    this.setTheme(saved === "light" || saved === "dark" ? saved : window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark");
    this.$("theme-toggle").onclick = () => {
      const theme = doc.documentElement.dataset.theme === "dark" ? "light" : "dark";
      this.setTheme(theme);
      try { localStorage.setItem("sandboxd.theme", theme); } catch { /* Still works for this page. */ }
    };
    doc.addEventListener("keydown", (event) => {
      if (event.ctrlKey || event.metaKey || event.altKey || event.repeat || doc.querySelector("dialog[open]")) return;
      const target = event.target;
      if (target.closest?.("input, textarea, select, button, a, [role='tab'], [contenteditable='true'], .xterm")) return;
      if (event.key === "/") { event.preventDefault(); this.$("resource-search").focus(); }
      if (event.key.toLowerCase() === "n") { event.preventDefault(); this.showCreate(); }
    });
  }

  showCreate() {
    if (!this.createDialog.open) this.createDialog.showModal();
    this.$("image-select").focus();
  }

  closeCreate() { if (!this.$("create-submit").disabled) this.createDialog.close(); }

  setTheme(theme) {
    this.doc.documentElement.dataset.theme = theme;
    const button = this.$("theme-toggle");
    button.setAttribute("aria-label", `Switch to ${theme === "dark" ? "light" : "dark"} theme`);
    button.title = button.getAttribute("aria-label");
    button.querySelector("use").setAttribute("href", `#i-${theme === "dark" ? "sun" : "moon"}`);
  }

  setView(view) {
    this.view = view;
    for (const name of ["sandboxes", "snapshots"]) {
      this.$(`nav-${name}`).setAttribute("aria-pressed", String(name === view));
    }
    this.$("sandboxes-panel").hidden = view !== "sandboxes";
    this.$("snapshots-view").hidden = view !== "snapshots";
    const label = `Find ${view === "sandboxes" ? "a sandbox" : "a snapshot"}`;
    this.$("resource-search").placeholder = `${label}…`;
    this.$("resource-search").setAttribute("aria-label", label);
    this.filter();
  }

  activeSandbox() {
    return this.data.sandboxes.find((sb) => sb.id === this.data.activeTab?.session.sandboxID && sb.state === "running");
  }

  update(data) {
    this.data = data;
    this.$("nav-sandbox-count").textContent = String(data.sandboxes.length);
    this.$("nav-snapshot-count").textContent = String(data.snapshots.length);
    this.$("snapshot-count").textContent = String(data.snapshots.length);
    this.$("snapshots-disconnected").hidden = !this.$("snapshots-panel").hidden;
    const running = data.sandboxes.filter((sb) => sb.state === "running").length;
    this.$("workspace-summary").textContent = `${running} running ${running === 1 ? "sandbox" : "sandboxes"}`;
    const capacity = describeCapacity(data.capacity);
    const readout = this.$("workspace-capacity");
    readout.textContent = capacity.text;
    readout.title = capacity.title;
    readout.hidden = !capacity.text;
    readout.classList.toggle("tight", capacity.tight);
    const active = data.activeTab;
    const sb = this.activeSandbox();
    this.$("workspace-kind").textContent = active ? active.kind === "run" ? "COMMAND & APP PREVIEW" : "INTERACTIVE TERMINAL" : "DEVELOPMENT WORKSPACE";
    this.$("workspace-name").textContent = active ? active.session.label : "Your development workspace";
    this.$("workspace-image").textContent = sb?.image || "";
    this.$("workspace-image").hidden = !sb?.image;
    for (const id of ["workspace-files", "workspace-run", "workspace-terminal"]) this.$(id).disabled = !sb;
    for (const row of this.$("sandbox-list").children) row.classList.toggle("selected", row.dataset.id === active?.session.sandboxID);
    this.filter();
  }

  filter() {
    const query = this.$("resource-search").value.trim().toLocaleLowerCase();
    for (const [listID, emptyID, values] of [["sandbox-list", "resource-no-results", this.data.sandboxes], ["snapshot-list", "snapshot-no-results", this.data.snapshots]]) {
      let visible = 0;
      for (const [index, row] of [...this.$(listID).children].entries()) {
        // Search metadata as well as shortened IDs; IDs and labels remain text.
        const value = values[index];
        const text = value ? JSON.stringify([value.id, value.image, value.sandbox_id, value.state, value.status, value.labels]) : row.textContent;
        row.hidden = !text.toLocaleLowerCase().includes(query);
        if (!row.hidden) visible++;
      }
      this.$(emptyID).hidden = !query || !values.length || visible > 0;
    }
  }
}
