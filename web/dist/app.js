// sandboxd web console: create, inspect, and kill sandboxes, and open
// interactive terminals in them.

import { api, engineReady, getAPIKey, setAPIKey } from "./api.js";
import { TerminalSession } from "./terminal.js";

const POLL_INTERVAL_MS = 3000;
// Open terminal tabs, so a reloaded page reattaches to their shells.
const TERMINALS_STORAGE = "sandboxd.terminals";

const $ = (selector) => document.querySelector(selector);

const state = {
  sandboxes: [],
  // Previous CPU samples, for usage between polls: id -> {micros, at}.
  cpuSamples: new Map(),
  stats: new Map(),
  terminals: [],
  activeTerminal: null,
  terminalCounters: new Map(),
  // Set after an auth failure was reported, so polling does not repeat it.
  authWarned: false,
};

// ---------- small DOM helpers ----------

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key === "class") node.className = value;
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else if (value !== undefined && value !== null) node.setAttribute(key, value);
  }
  for (const child of children.flat()) {
    if (child !== null && child !== undefined) node.append(child);
  }
  return node;
}

function toast(message, kind = "info") {
  const node = el("div", { class: `toast ${kind}`, role: kind === "error" ? "alert" : "status" }, message);
  $("#toasts").append(node);
  setTimeout(() => node.remove(), kind === "error" ? 7000 : 3500);
}

function shortID(id) {
  return id.length > 16 ? `${id.slice(0, 12)}…${id.slice(-4)}` : id;
}

function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(value >= 10 || unit === 0 ? 0 : 1)} ${units[unit]}`;
}

function formatRemaining(isoTime) {
  const seconds = Math.round((new Date(isoTime) - Date.now()) / 1000);
  if (seconds <= 0) return "expiring";
  if (seconds < 90) return `${seconds}s left`;
  if (seconds < 5400) return `${Math.round(seconds / 60)}m left`;
  return `${(seconds / 3600).toFixed(1)}h left`;
}

// ---------- engine status ----------

async function refreshEngineStatus() {
  const ready = await engineReady();
  const status = $("#engine-status");
  status.dataset.state = ready ? "ready" : "down";
  status.querySelector("span").textContent = ready ? "engine ready" : "engine unavailable";
}

// ---------- sandbox list ----------

async function refreshSandboxes() {
  let sandboxes;
  try {
    sandboxes = await api("GET", "/sandboxes");
  } catch (err) {
    if (err.status === 401 && !state.authWarned) {
      state.authWarned = true;
      toast("Unauthorized: set the API key.", "error");
    }
    return;
  }
  state.authWarned = false;
  state.sandboxes = (sandboxes || []).sort((a, b) => new Date(b.created_at) - new Date(a.created_at));

  const running = state.sandboxes.filter((sb) => sb.state === "running");
  const results = await Promise.allSettled(running.map((sb) => api("GET", `/sandboxes/${encodeURIComponent(sb.id)}/stats`)));
  const now = performance.now();
  results.forEach((result, index) => {
    const sb = running[index];
    if (result.status !== "fulfilled" || !result.value) return;
    const stats = result.value;
    const previous = state.cpuSamples.get(sb.id);
    if (previous) {
      const elapsedMicros = (now - previous.at) * 1000;
      const cores = sb.limits.cpu_millis / 1000;
      stats.cpuPercent = Math.min(100, Math.max(0, ((stats.cpu_usage_micros - previous.micros) / (elapsedMicros * cores)) * 100));
    }
    state.cpuSamples.set(sb.id, { micros: stats.cpu_usage_micros, at: now });
    state.stats.set(sb.id, stats);
  });
  // Terminals of sandboxes that stopped can never come back.
  for (const entry of state.terminals) {
    const sb = state.sandboxes.find((candidate) => candidate.id === entry.session.sandboxID);
    if (!sb || sb.state !== "running") entry.session.markGone();
  }
  for (const id of state.cpuSamples.keys()) {
    if (!running.some((sb) => sb.id === id)) {
      state.cpuSamples.delete(id);
      state.stats.delete(id);
    }
  }
  renderSandboxes();
}

function meter(label, value, fraction) {
  const bar = el("div", { class: `bar${fraction > 0.85 ? " hot" : ""}` }, el("i"));
  bar.firstChild.style.width = `${Math.round(Math.min(1, Math.max(0, fraction || 0)) * 100)}%`;
  return el("div", { class: "meter" }, el("span", {}, `${label} `, el("b", {}, value)), bar);
}

function renderSandbox(sb) {
  const stats = state.stats.get(sb.id);
  const meta = [sb.image, sb.network, sb.ip_address].filter(Boolean).join(" · ");
  const running = sb.state === "running";

  const meters = running && stats
    ? el(
        "div",
        { class: "meters" },
        meter("CPU", stats.cpuPercent === undefined ? "…" : `${stats.cpuPercent.toFixed(0)}%`, (stats.cpuPercent || 0) / 100),
        meter("Mem", `${formatBytes(stats.memory_bytes)}`, stats.memory_bytes / (sb.limits.memory_mb * 1024 * 1024)),
        meter("Disk", formatBytes(stats.storage_used_bytes), stats.storage_used_bytes / stats.storage_limit_bytes),
      )
    : null;

  const reason = sb.failure_reason || sb.stop_reason;
  return el(
    "li",
    { class: "sandbox", "data-id": sb.id },
    el(
      "div",
      { class: "sandbox-top" },
      el("span", { class: "sandbox-id", title: sb.id }, shortID(sb.id)),
      el("span", { class: `badge ${sb.state}` }, sb.state),
    ),
    el(
      "div",
      { class: "sandbox-meta" },
      meta,
      running ? ` · ${formatRemaining(sb.expires_at)}` : "",
      running && stats ? ` · ${stats.pids_current} procs` : "",
      !running && reason ? ` · ${reason}` : "",
    ),
    meters,
    running
      ? el(
          "div",
          { class: "actions" },
          el("button", { class: "btn small primary", type: "button", onclick: () => openTerminal(sb.id) }, "Terminal"),
          el("button", { class: "btn small", type: "button", onclick: () => keepalive(sb.id) }, "Keepalive"),
          el("button", { class: "btn small danger", type: "button", onclick: () => killSandbox(sb.id) }, "Kill"),
        )
      : null,
  );
}

function renderSandboxes() {
  const list = $("#sandbox-list");
  list.replaceChildren(...state.sandboxes.map(renderSandbox));
  const running = state.sandboxes.filter((sb) => sb.state === "running").length;
  $("#sandbox-count").textContent = state.sandboxes.length ? `${running} running` : "";
  $("#sandbox-empty").hidden = state.sandboxes.length > 0;
}

// ---------- actions ----------

async function loadImages() {
  try {
    const images = (await api("GET", "/images")) || [];
    const select = $("#image-select");
    select.replaceChildren(el("option", { value: "" }, "default"), ...images.map((image) => el("option", { value: image.name }, image.name)));
  } catch {
    // The default image still works.
  }
}

function readCreateForm(form) {
  const data = new FormData(form);
  const body = { network: data.get("network") || "none" };
  if (data.get("image")) body.image = data.get("image");
  for (const field of ["cpu_millis", "memory_mb", "storage_mb", "pids_max", "ttl_seconds", "idle_timeout_seconds"]) {
    const value = data.get(field);
    if (value !== "" && value !== null) body[field] = Number(value);
  }
  const labels = String(data.get("labels") || "").trim();
  if (labels) {
    body.labels = {};
    for (const pair of labels.split(",")) {
      const [key, ...rest] = pair.split("=");
      if (key.trim()) body.labels[key.trim()] = rest.join("=").trim();
    }
  }
  return body;
}

async function createSandbox(event) {
  event.preventDefault();
  const submit = $("#create-submit");
  submit.disabled = true;
  submit.textContent = "Creating…";
  try {
    const sb = await api("POST", "/sandboxes", readCreateForm(event.target));
    toast(`Created ${shortID(sb.id)}`);
    await refreshSandboxes();
    openTerminal(sb.id);
  } catch (err) {
    toast(`Create failed: ${err.message}`, "error");
  } finally {
    submit.disabled = false;
    submit.textContent = "Create sandbox";
  }
}

async function keepalive(id) {
  try {
    await api("POST", `/sandboxes/${encodeURIComponent(id)}/keepalive`, {});
    toast(`Extended ${shortID(id)}`);
    refreshSandboxes();
  } catch (err) {
    toast(`Keepalive failed: ${err.message}`, "error");
  }
}

async function killSandbox(id) {
  if (!window.confirm(`Kill sandbox ${id}? Everything inside it is lost.`)) return;
  try {
    await api("DELETE", `/sandboxes/${encodeURIComponent(id)}`);
    toast(`Killed ${shortID(id)}`);
  } catch (err) {
    toast(`Kill failed: ${err.message}`, "error");
  }
  refreshSandboxes();
}

// ---------- terminals ----------

function saveTerminals() {
  const saved = state.terminals
    .filter((entry) => entry.session.terminalID)
    .map(({ session }) => ({ sandboxID: session.sandboxID, terminalID: session.terminalID, label: session.label }));
  try {
    localStorage.setItem(TERMINALS_STORAGE, JSON.stringify(saved));
  } catch {
    // Storage unavailable: tabs simply are not restored after a reload.
  }
}

function savedTerminals() {
  try {
    const saved = JSON.parse(localStorage.getItem(TERMINALS_STORAGE) || "[]");
    return Array.isArray(saved) ? saved.filter((item) => item?.sandboxID && item?.terminalID) : [];
  } catch {
    return [];
  }
}

// openTerminal opens a tab with a new shell, or with terminalID reattaches
// to a shell still running from an earlier page load.
function openTerminal(sandboxID, { terminalID = null, label = null } = {}) {
  if (typeof window.Terminal !== "function") {
    toast("Terminal emulator failed to load.", "error");
    return;
  }
  const restoredCount = Number(label?.match(/#(\d+)$/)?.[1]) || 0;
  const count = Math.max((state.terminalCounters.get(sandboxID) || 0) + 1, restoredCount);
  state.terminalCounters.set(sandboxID, count);
  label = label || `${shortID(sandboxID)} #${count}`;

  const pane = el("div", { class: "pane", role: "tabpanel" });
  $("#panes").append(pane);
  const closeButton = el("button", { class: "close", type: "button", "aria-label": `Close ${label}`, title: "Close terminal" }, "×");
  const tab = el("div", { class: "tab", role: "tab", tabindex: "0", title: sandboxID }, el("span", { class: "dot" }), label, closeButton);
  $("#tabs").append(tab);

  const entry = { tab, pane, session: null };
  entry.session = new TerminalSession(sandboxID, label, pane, {
    terminalID,
    onStateChange: (session) => tab.classList.toggle("closed", session.ended),
    onAttached: saveTerminals,
  });
  state.terminals.push(entry);

  tab.addEventListener("click", () => activateTerminal(entry));
  tab.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") activateTerminal(entry);
  });
  closeButton.addEventListener("click", (event) => {
    event.stopPropagation();
    closeTerminal(entry);
  });

  activateTerminal(entry);
  entry.session.connect();
}

function activateTerminal(entry) {
  state.activeTerminal = entry;
  for (const other of state.terminals) {
    const active = other === entry;
    other.tab.setAttribute("aria-selected", String(active));
    other.pane.classList.toggle("active", active);
  }
  $("#workspace-empty").hidden = true;
  entry.session.focus();
}

// closeTerminal closes the tab and ends its shell.
function closeTerminal(entry) {
  entry.session.dispose();
  entry.tab.remove();
  entry.pane.remove();
  const index = state.terminals.indexOf(entry);
  state.terminals.splice(index, 1);
  saveTerminals();
  if (state.activeTerminal === entry) {
    const next = state.terminals[Math.min(index, state.terminals.length - 1)];
    if (next) activateTerminal(next);
    else {
      state.activeTerminal = null;
      $("#workspace-empty").hidden = false;
    }
  }
}

// ---------- settings ----------

function setupSettings() {
  const dialog = $("#settings");
  const input = $("#api-key");
  $("#settings-open").addEventListener("click", () => {
    input.value = getAPIKey();
    dialog.showModal();
  });
  dialog.addEventListener("close", () => {
    if (dialog.returnValue !== "save") return;
    setAPIKey(input.value.trim());
    toast(input.value.trim() ? "API key saved" : "API key cleared");
    loadImages();
    refreshSandboxes();
  });
}

// ---------- start ----------

function start() {
  setupSettings();
  $("#create-form").addEventListener("submit", createSandbox);
  refreshEngineStatus();
  loadImages();
  refreshSandboxes();
  for (const saved of savedTerminals()) openTerminal(saved.sandboxID, saved);
  setInterval(() => {
    if (document.hidden) return;
    refreshSandboxes();
    refreshEngineStatus();
  }, POLL_INTERVAL_MS);
}

start();
