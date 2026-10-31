// sandboxd web console: create, inspect, and kill sandboxes, and open
// interactive terminals and run tabs in them.

import { api, engineReady, getAPIKey, setAPIKey } from "./api.js";
import { el } from "./dom.js";
import { formatBytes } from "./file-utils.js";
import { describeJob } from "./job-progress.js";
import { TerminalSession } from "./terminal.js";
import { RunSession } from "./run.js";
import { setupCloudStorage } from "./cloud-storage.js";
import { setupWorkspaceTools } from "./workspace-tools.js";
import { FileExplorer } from "./file-explorer.js";
import { confirmAction, promptValue } from "./dialogs.js";
import { setupSchedulePickers } from "./schedule-picker.js";
import { ConsoleUI, describeImage, sandboxMeta, icon } from "./console-ui.js";

const POLL_INTERVAL_MS = 3000;
// Running backups and restores are read from the daemon's memory, so this is
// cheap to poll. Listing snapshots is not: it reads every manifest from cloud
// storage, which can take seconds against a remote bucket.
const JOBS_INTERVAL_MS = 1000;
// Open tabs, so a reloaded page reattaches to their shells and commands.
const TABS_STORAGE = "sandboxd.terminals";

const $ = (selector) => document.querySelector(selector);

const state = {
  sandboxes: [],
  // Previous CPU samples, for usage between polls: id -> {micros, at}.
  cpuSamples: new Map(),
  stats: new Map(),
  // Workspace tabs, terminals and run tabs alike.
  tabs: [],
  activeTab: null,
  tabCounters: new Map(),
  // Set after an auth failure was reported, so polling does not repeat it.
  authWarned: false,
  images: [],
  hostArchitecture: "",
  capacity: null,
  nodes: new Set(),
  snapshotsEnabled: false,
  snapshots: [],
  snapshotRefreshPending: false,
  snapshotsPolledAt: 0,
};

// ---------- small DOM helpers ----------

function toast(message, kind = "info") {
  const node = el("div", { class: `toast ${kind}`, role: kind === "error" ? "alert" : "status" }, message);
  $("#toasts").append(node);
  setTimeout(() => node.remove(), kind === "error" ? 7000 : 3500);
}

function shortID(id) {
  return id.length > 16 ? `${id.slice(0, 12)}…${id.slice(-4)}` : id;
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
  status.querySelector("span").textContent = ready ? "Engine online" : "Engine unavailable";
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
  state.nodes = new Set(state.sandboxes.map((sb) => sb.node).filter(Boolean));

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
  // Tabs of sandboxes that stopped can never come back.
  for (const entry of state.tabs) {
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
  fileExplorer.update(state.sandboxes);
}

// refreshCapacity reads what this daemon can still take on. It is advisory:
// the console works without it, so a failure leaves the last reading alone.
async function refreshCapacity() {
  try {
    state.capacity = await api("GET", "/capacity");
    state.hostArchitecture = state.capacity?.architecture || state.hostArchitecture;
    updateConsoleUI();
  } catch {
    // The next poll tries again.
  }
}

function meter(label, value, fraction) {
  const bar = el("div", { class: `bar${fraction > 0.85 ? " hot" : ""}` }, el("i"));
  bar.firstChild.style.width = `${Math.round(Math.min(1, Math.max(0, fraction || 0)) * 100)}%`;
  return el("div", { class: "meter" }, el("span", {}, `${label} `, el("b", {}, value)), bar);
}

function renderSandbox(sb) {
  const stats = state.stats.get(sb.id);
  const meta = sandboxMeta(sb, state.nodes.size);
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
    el("div", { class: "sandbox-top" },
      el("span", { class: "sandbox-symbol" }, icon("box")),
      el("div", { class: "sandbox-identity" },
        el("span", { class: "sandbox-id", title: sb.id }, sb.labels?.name || shortID(sb.id)),
        el("span", { class: "sandbox-image" }, sb.image || "Default image")),
      el("span", { class: `badge ${sb.state}` }, sb.state)),
    el(
      "div",
      { class: "sandbox-meta" },
      meta,
      running ? ` · ${formatRemaining(sb.expires_at)}` : "",
      running && stats ? ` · ${stats.pids_current} procs` : "",
      !running && reason ? ` · ${reason}` : "",
    ),
    meters,
    running && (sb.snapshot_policy?.interval_seconds || sb.snapshot_policy?.before_expiry) ? el("div", { class: "sandbox-meta" },
      sb.snapshot_in_progress ? "Backing up…" : [sb.snapshot_policy.interval_seconds ? `Scheduled every ${sb.snapshot_policy.interval_seconds / 60} min` : null, sb.snapshot_policy.before_expiry ? "Backup before expiry" : null].filter(Boolean).join(" · "),
      sb.next_snapshot_at ? ` · Next ${new Date(sb.next_snapshot_at).toLocaleTimeString()}` : "") : null,
    sb.last_snapshot_error ? el("div", { class: "backup-error", role: "status" }, sb.last_snapshot_error) : null,
    running
      ? el(
          "div",
          { class: "actions" },
          el("button", { class: "btn small terminal-action", type: "button", onclick: () => openTerminal(sb.id) }, icon("terminal"), "Terminal"),
          el("button", { class: "btn small ghost", type: "button", onclick: () => fileExplorer.open(sb) }, icon("folder"), "Files"),
          el("button", { class: "btn small ghost", type: "button", onclick: () => openRun(sb.id) }, icon("play"), "Run"),
        )
      : null,
    running ? el("details", {
        class: "sandbox-manage",
        // Previews live in the daemon, not in this page: one opened from
        // another tab or left by a reload is invisible here until asked for.
        // Ask when the panel opens rather than on every poll.
        ontoggle: (event) => { if (event.currentTarget.open) void loadPreviews(sb.id, event.currentTarget); },
      },
        el("summary", {}, "Manage environment", icon("more")),
        el("div", { class: "preview-sessions", "data-previews": sb.id }),
        el("div", { class: "actions management-actions" },
          el("button", { class: "btn small", type: "button", onclick: () => openRun(sb.id, { focus: "port" }) }, "App preview"),
          state.snapshotsEnabled ? el("button", { class: "btn small", type: "button", onclick: () => workspaceTools.openBackupPolicy(sb) }, "Auto backups") : null,
          state.snapshotsEnabled ? el("button", { class: "btn small", type: "button", disabled: sb.snapshot_in_progress ? "" : null, onclick: (event) => checkpointSandbox(sb.id, event.currentTarget) }, "Snapshot") : null,
          el("button", { class: "btn small", type: "button", onclick: () => keepalive(sb.id) }, "Keepalive"),
          state.snapshotsEnabled ? el("button", { class: "btn small", type: "button", onclick: (event) => saveSnapshot(sb.id, event.currentTarget) }, "Save & stop") : null,
          el("button", { class: "btn small danger", type: "button", onclick: () => killSandbox(sb.id) }, "Kill"),
        ))
      : state.snapshotsEnabled && sb.state === "failed" && sb.stop_reason === "snapshot" && !sb.stopped_at
        ? el("div", { class: "actions" },
          el("button", { class: "btn small", type: "button", onclick: (event) => saveSnapshot(sb.id, event.currentTarget) }, "Retry save"),
          el("button", { class: "btn small danger", type: "button", onclick: () => killSandbox(sb.id) }, "Delete source"))
        : null,
  );
}

function renderSandboxes() {
  const list = $("#sandbox-list");
  const focused = list.contains(document.activeElement) ? {
    id: document.activeElement.closest(".sandbox")?.dataset.id,
    tag: document.activeElement.tagName.toLowerCase(),
    text: document.activeElement.textContent,
  } : null;
  const expanded = new Set([...list.querySelectorAll(".sandbox-manage[open]")].map((node) => node.closest(".sandbox").dataset.id));
  list.replaceChildren(...state.sandboxes.map(renderSandbox));
  for (const row of list.children) {
    if (expanded.has(row.dataset.id)) row.querySelector(".sandbox-manage")?.setAttribute("open", "");
    if (row.dataset.id === focused?.id) {
      [...row.querySelectorAll(focused.tag)].find((node) => node.textContent === focused.text)?.focus({ preventScroll: true });
    }
  }
  const running = state.sandboxes.filter((sb) => sb.state === "running").length;
  $("#sandbox-count").textContent = state.sandboxes.length ? `${running} running` : "";
  $("#sandbox-empty").hidden = state.sandboxes.length > 0;
  updateConsoleUI();
}

function updateConsoleUI() {
  consoleUI.update({ sandboxes: state.sandboxes, snapshots: state.snapshots, activeTab: state.activeTab, snapshotsEnabled: state.snapshotsEnabled, capacity: state.capacity });
}

// ---------- actions ----------

// loadImages fills the picker with what this host can start from. A promoted
// image the host has not built yet is offered, because it will be built on
// first use; one promoted on another architecture is shown but not offered,
// because asking for it could only fail.
async function loadImages() {
  try {
    const images = (await api("GET", "/images")) || [];
    state.images = images;
    // The host's architecture decides which cloud images are usable here.
    // Without it every image stays selectable, which is the old behaviour.
    try {
      state.hostArchitecture = (await api("GET", "/capacity"))?.architecture || "";
    } catch {
      state.hostArchitecture = "";
    }
    const select = $("#image-select");
    const chosen = select.value;
    select.replaceChildren(el("option", { value: "" }, "Default image"), ...images.map((image) => {
      const described = describeImage(image, state.hostArchitecture);
      return el("option", { value: image.name, disabled: described.disabled ? "" : null }, described.label);
    }));
    if ([...select.options].some((option) => option.value === chosen && !option.disabled)) select.value = chosen;
    showImageHint();
  } catch {
    // The default image still works.
  }
}

// showImageHint explains the one thing a picker cannot: choosing an image
// this host has never held makes the next create take as long as a restore.
function showImageHint() {
  const hint = $("#image-hint");
  const image = state.images.find((item) => item.name === $("#image-select").value);
  const remote = image && image.local === false;
  hint.hidden = !remote;
  if (remote) {
    hint.textContent = "This host does not have that image yet. The first sandbox using it downloads and unpacks it, which takes about as long as restoring a snapshot. Later ones start normally.";
  }
}

// renderImages lists every image this deployment can start from, so a
// promoted one can be removed -- which is also what frees the snapshot
// underneath it for deletion.
async function renderImages() {
  const list = $("#image-list");
  try {
    const images = (await api("GET", "/images")) || [];
    state.images = images;
    $("#image-status").textContent = images.length ? "" : "No base images. Import one with sandboxdctl, or promote a snapshot.";
    list.replaceChildren(...images.map((image) => el("li", { class: "image-row" },
      el("div", {},
        el("div", { class: "image-name" }, image.name),
        el("div", { class: "sandbox-meta" }, [
          image.local ? "on this host" : "in cloud storage",
          image.architecture || "",
          image.created_at ? new Date(image.created_at).toLocaleDateString() : "",
        ].filter(Boolean).join(" · "))),
      el("button", {
        class: "btn small danger",
        type: "button",
        "aria-label": `Remove image ${image.name}`,
        onclick: (event) => removeImage(image.name, event.currentTarget),
      }, "Remove"))));
  } catch (err) {
    $("#image-status").textContent = `Images unavailable: ${err.message}`;
    list.replaceChildren();
  }
}

async function removeImage(name, button) {
  if (!await confirmAction(`Remove base image ${name}? Sandboxes already running on it keep working. If it was promoted from a snapshot, it is removed from cloud storage too, so other daemons stop resolving the name.`, { title: "Remove base image?", confirmLabel: "Remove image" })) return;
  button.disabled = true;
  try {
    await api("DELETE", `/images/${encodeURIComponent(name)}`);
    toast(`Removed ${name}`);
    await Promise.all([renderImages(), loadImages()]);
  } catch (err) {
    toast(`Remove failed: ${err.message}`, "error");
    button.disabled = false;
  }
}

async function checkpointSandbox(id, button) {
  button.disabled = true;
  try {
    await api("POST", `/sandboxes/${encodeURIComponent(id)}/snapshots`, { keep_running: true });
    toast("Snapshot started. Processes resume after the filesystem copy.");
    await refreshSandboxes();
    await refreshSnapshots(true);
  } catch (err) { toast(err.message, "error"); }
  finally { button.disabled = false; }
}

function readCreateForm(form) {
  const data = new FormData(form);
  const body = { network: data.get("network") || "none" };
  if (Number(data.get("backup_interval")) || data.has("backup_before_expiry")) {
    body.snapshot_policy = { interval_seconds: Number(data.get("backup_interval")), before_expiry: data.has("backup_before_expiry"), retain: Number(data.get("backup_retain")) || 5 };
  }
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
    $("#create-dialog").close();
  } catch (err) {
    toast(`Create failed: ${err.message}`, "error");
  } finally {
    submit.disabled = false;
    submit.textContent = "Create sandbox";
  }
}

// loadPreviews shows the previews this daemon holds for a sandbox, so one
// left open by a closed tab can be found and closed instead of occupying a
// session slot until the sandbox ends.
async function loadPreviews(id, panel) {
  const host = panel.querySelector(`[data-previews="${CSS.escape(id)}"]`);
  if (!host) return;
  host.replaceChildren(el("p", { class: "sandbox-meta" }, "Checking for open previews…"));
  let sessions;
  try {
    sessions = (await api("GET", `/sandboxes/${encodeURIComponent(id)}/previews`)) || [];
  } catch (err) {
    host.replaceChildren(el("p", { class: "sandbox-meta" }, `Previews unavailable: ${err.message}`));
    return;
  }
  if (!sessions.length) {
    host.replaceChildren(el("p", { class: "sandbox-meta" }, "No previews open."));
    return;
  }
  host.replaceChildren(
    el("p", { class: "sandbox-meta" }, `${sessions.length} preview${sessions.length === 1 ? "" : "s"} open. Anyone holding the URL can reach the app.`),
    ...sessions.map((session) => el("div", { class: "preview-session" },
      el("a", { class: "preview-link", href: session.url, target: "_blank", rel: "noreferrer noopener" }, `Port ${session.port}`),
      el("span", { class: "sandbox-meta" }, `expires ${new Date(session.expires_at).toLocaleTimeString()}`),
      el("button", {
        class: "btn small danger",
        type: "button",
        "aria-label": `Close the preview on port ${session.port}`,
        onclick: (event) => void closePreview(id, session.id, event.currentTarget, panel),
      }, "Close"))),
  );
}

async function closePreview(id, previewID, button, panel) {
  button.disabled = true;
  try {
    await api("DELETE", `/sandboxes/${encodeURIComponent(id)}/previews`, { id: previewID });
    toast("Preview closed");
    await loadPreviews(id, panel);
  } catch (err) {
    toast(`Close failed: ${err.message}`, "error");
    button.disabled = false;
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
  if (!await confirmAction(`This will stop all processes in ${id} and delete its filesystem. This cannot be undone.`, { title: "Kill sandbox?", confirmLabel: "Kill sandbox" })) return;
  try {
    await api("DELETE", `/sandboxes/${encodeURIComponent(id)}`);
    toast(`Killed ${shortID(id)}`);
  } catch (err) {
    toast(`Kill failed: ${err.message}`, "error");
  }
  refreshSandboxes();
}

// ---------- filesystem snapshots ----------

async function refreshSnapshots(force = false) {
  if (state.snapshotRefreshPending) return;
  // Listing snapshots costs a cloud round trip per manifest, so it keeps a
  // slow cadence even while a job runs; the progress bar is fed by
  // refreshSnapshotJobs instead.
  const active = state.snapshots.some((item) => item.progress || item.status === "capturing" || item.status === "uploading")
    || state.sandboxes.some((sb) => sb.snapshot_in_progress);
  const interval = active ? 10000 : 60000;
  if (!force && Date.now() - state.snapshotsPolledAt < interval) return;
  state.snapshotRefreshPending = true;
  state.snapshotsPolledAt = Date.now();
  try {
    state.snapshots = (await api("GET", "/snapshots")) || [];
    state.snapshotsEnabled = true;
    $("#snapshots-panel").hidden = false;
    $("#snapshots-status").textContent = state.snapshots.length ? "Restore creates a new sandbox with a fresh lifetime." : "Save a sandbox to restore its files later.";
    $("#snapshot-list").replaceChildren(...state.snapshots.map(renderSnapshot));
    renderSandboxes();
  } catch (err) {
    if (err.code === "snapshot_disabled") {
      state.snapshotsEnabled = false;
      state.snapshots = [];
      $("#snapshot-list").replaceChildren();
      $("#snapshots-panel").hidden = true;
      renderSandboxes();
    } else if (err.code === "snapshot_storage_unavailable") {
      $("#snapshots-panel").hidden = false;
      $("#snapshots-status").textContent = "Cloud storage is unavailable. Check the server's snapshot configuration.";
    } else if (state.snapshotsEnabled) {
      $("#snapshots-status").textContent = "Snapshot status unavailable. Displayed data may be out of date.";
    }
  } finally { state.snapshotRefreshPending = false; updateConsoleUI(); }
}

// snapshotProgress renders one running job: what it is doing, how far it has
// got, and a bar once the total is known. A capture has no total, so it shows
// the bytes written under a bar that only says "still working".
function snapshotProgress(progress) {
  const job = describeJob(progress);
  const known = job.percent !== null;
  const bar = el("div", { class: `bar${known ? "" : " indeterminate"}` }, el("i"));
  if (known) bar.firstChild.style.width = `${job.percent}%`;
  return el("div", { class: "job", role: "status" },
    el("div", { class: "job-head" },
      el("span", {}, job.label),
      el("b", {}, known ? `${job.percent}%` : job.amount)),
    bar,
    known ? el("div", { class: "sandbox-meta" }, job.amount) : null);
}

// refreshSnapshotJobs updates the progress of running jobs without touching
// cloud storage. When the last job finishes, the snapshot list is refreshed
// once so the card settles on its final status.
async function refreshSnapshotJobs() {
  if (document.hidden || !state.snapshotsEnabled) return;
  let jobs;
  try {
    jobs = await api("GET", "/snapshots/progress");
  } catch {
    return; // The next tick tries again; the list keeps its own cadence.
  }
  const byID = new Map((jobs || []).map((job) => [job.snapshot_id, job.progress]));
  const had = state.snapshots.some((item) => item.progress);
  let changed = false;
  for (const item of state.snapshots) {
    const progress = byID.get(item.id) || null;
    if (JSON.stringify(progress) === JSON.stringify(item.progress || null)) continue;
    item.progress = progress;
    changed = true;
  }
  if (!changed) return;
  $("#snapshot-list").replaceChildren(...state.snapshots.map(renderSnapshot));
  updateConsoleUI();
  // A job that just ended leaves a stale status on the card.
  if (had && !byID.size) await refreshSnapshots(true);
}

function renderSnapshot(item) {
  const ready = item.status === "ready";
  const finished = ready || item.status === "failed";
  const restoring = item.progress?.operation === "restore";
  const promoting = item.progress?.operation === "promote";
  const busy = restoring || promoting;
  const label = restoring ? "restoring" : promoting ? "promoting" : item.status;
  return el("li", { class: "sandbox" },
    el("div", { class: "sandbox-top" },
      el("span", { class: "sandbox-id", title: item.id }, shortID(item.id)),
      el("span", { class: `badge ${label}` }, label)),
    el("div", { class: "sandbox-meta" },
      [shortID(item.sandbox_id), item.architecture, item.size_bytes ? formatBytes(item.size_bytes) : "", new Date(item.created_at).toLocaleString()].filter(Boolean).join(" · ")),
    item.progress ? snapshotProgress(item.progress) : null,
    item.failure_reason ? el("div", { class: "sandbox-meta" }, item.failure_reason) : null,
    el("div", { class: "actions" },
      ready ? el("button", { class: "btn small primary", type: "button", disabled: busy ? "" : null, onclick: (event) => restoreSnapshot(item.id, event.currentTarget) }, restoring ? "Restoring…" : "Restore") : null,
      ready ? el("button", { class: "btn small", type: "button", disabled: busy ? "" : null, title: "Import these files as a base image, so new sandboxes start from them", onclick: (event) => promoteSnapshot(item.id, event.currentTarget) }, promoting ? "Importing…" : "Use as image") : null,
      finished ? el("button", { class: "btn small danger", type: "button", disabled: item.progress ? "" : null, onclick: (event) => deleteSnapshot(item.id, event.currentTarget) }, "Delete backup") : null));
}

async function saveSnapshot(id, button) {
  if (!await confirmAction(`Running commands and terminals in ${id} will end. Files will be backed up to cloud storage.`, { title: "Save and stop sandbox?", confirmLabel: "Save and stop" })) return;
  button.disabled = true;
  try {
    await api("POST", `/sandboxes/${encodeURIComponent(id)}/snapshots`, {});
    toast("Snapshot started. The sandbox is stopping.");
    await Promise.all([refreshSandboxes(), refreshSnapshots(true)]);
  } catch (err) { toast(`Save failed: ${err.message}`, "error"); }
  finally { button.disabled = false; }
}

async function restoreSnapshot(id, button) {
  button.disabled = true;
  button.textContent = "Restoring…";
  try {
    const sb = await api("POST", "/sandboxes", { snapshot_id: id });
    toast(`Restored ${shortID(sb.id)}`);
    await refreshSandboxes();
    openTerminal(sb.id);
  } catch (err) { toast(`Restore failed: ${err.message}`, "error"); }
  finally {
    button.disabled = false;
    button.textContent = "Restore";
    await refreshSnapshots(true);
  }
}

// promoteSnapshot turns a backup into a base image, so whatever this sandbox
// had installed becomes the starting point of the next one at no cost.
async function promoteSnapshot(id, button) {
  const name = await promptValue("New sandboxes can start from this snapshot. Use lowercase letters, digits, dots, dashes, or underscores.", {
    title: "Create a base image", confirmLabel: "Create image",
    input: { label: "Image name", placeholder: "my-base-image", pattern: "[a-z0-9][a-z0-9._\\-]{0,63}", maxLength: 64 },
  });
  if (name === null) return;
  button.disabled = true;
  button.textContent = "Importing…";
  try {
    const image = await api("POST", `/snapshots/${encodeURIComponent(id)}/image`, { image: name.trim() });
    toast(`Image ${image.name} is ready to create sandboxes from.`);
    await loadImages();
  } catch (err) { toast(`Import failed: ${err.message}`, "error"); }
  finally {
    button.disabled = false;
    button.textContent = "Use as image";
    await refreshSnapshots(true);
  }
}

async function deleteSnapshot(id, button) {
  if (!await confirmAction(`This permanently removes the saved filesystem for ${id} from cloud storage.`, { title: "Delete backup?", confirmLabel: "Delete backup" })) return;
  button.disabled = true;
  try {
    await api("DELETE", `/snapshots/${encodeURIComponent(id)}`);
    toast("Backup deleted");
    await refreshSnapshots(true);
  } catch (err) { toast(`Delete failed: ${err.message}`, "error"); }
  finally { button.disabled = false; }
}

// ---------- workspace tabs ----------

// A tab is a shell the user types in, or a run tab: one command's log beside
// the app it serves. Both reattach after a page reload.

function saveTabs() {
  const saved = state.tabs
    .map(({ kind, session }) => (kind === "run" ? session.saved() : { kind, sandboxID: session.sandboxID, terminalID: session.terminalID, label: session.label }))
    // A terminal is only worth restoring once it has a shell to reattach to;
    // a run tab also carries its command and the port it was showing.
    .filter((item) => item.terminalID || (item.kind === "run" && (item.command || item.port)));
  try {
    localStorage.setItem(TABS_STORAGE, JSON.stringify(saved));
  } catch {
    // Storage unavailable: tabs simply are not restored after a reload.
  }
}

function savedTabs() {
  try {
    const saved = JSON.parse(localStorage.getItem(TABS_STORAGE) || "[]");
    if (!Array.isArray(saved)) return [];
    // Tabs stored before run tabs existed have no kind and are terminals.
    return saved.filter((item) => item?.sandboxID).map((item) => ({ ...item, kind: item.kind === "run" ? "run" : "terminal" }));
  } catch {
    return [];
  }
}

// addTab builds the tab and its pane, and hands the pane to the session that
// fills it.
function addTab(kind, sandboxID, label, build) {
  if (typeof window.Terminal !== "function") {
    toast("Terminal emulator failed to load.", "error");
    return null;
  }
  const counterKey = `${kind}:${sandboxID}`;
  const restoredCount = Number(label?.match(/#(\d+)$/)?.[1]) || 0;
  const count = Math.max((state.tabCounters.get(counterKey) || 0) + 1, restoredCount);
  state.tabCounters.set(counterKey, count);
  label = label || `${kind === "run" ? "run " : ""}${shortID(sandboxID)} #${count}`;

  const tabID = `tab-${kind}-${sandboxID}-${count}`;
  const paneID = `pane-${kind}-${sandboxID}-${count}`;
  const pane = el("div", { class: `pane ${kind}-pane`, role: "tabpanel", id: paneID, "aria-labelledby": tabID });
  $("#panes").append(pane);
  const closeButton = el("button", { class: "close", type: "button", "aria-label": `Close ${label}`, title: `Close ${kind === "run" ? "run tab" : "terminal"}` }, "×");
  const tab = el("div", { class: `tab ${kind}-tab`, role: "tab", id: tabID, "aria-controls": paneID, tabindex: "0", title: sandboxID }, el("span", { class: "dot" }), label, closeButton);
  $("#tabs").append(tab);

  const entry = { kind, tab, pane, session: null };
  entry.session = build(pane, label, (session) => tab.classList.toggle("closed", session.ended));
  state.tabs.push(entry);

  tab.addEventListener("click", () => activateTab(entry));
  tab.addEventListener("keydown", (event) => {
    if (event.target !== tab) return;
    if (event.key === "Enter" || event.key === " ") { event.preventDefault(); activateTab(entry); }
    const index = state.tabs.indexOf(entry);
    const next = event.key === "ArrowRight" ? state.tabs[(index + 1) % state.tabs.length]
      : event.key === "ArrowLeft" ? state.tabs[(index - 1 + state.tabs.length) % state.tabs.length]
        : event.key === "Home" ? state.tabs[0] : event.key === "End" ? state.tabs.at(-1) : null;
    if (next) { event.preventDefault(); activateTab(next); next.tab.focus(); }
  });
  closeButton.addEventListener("click", (event) => {
    event.stopPropagation();
    closeTab(entry);
  });

  activateTab(entry);
  return entry;
}

// openTerminal opens a tab with a new shell, or with terminalID reattaches
// to a shell still running from an earlier page load.
function openTerminal(sandboxID, { terminalID = null, label = null } = {}) {
  const entry = addTab("terminal", sandboxID, label, (pane, tabLabel, onStateChange) =>
    new TerminalSession(sandboxID, tabLabel, pane, { terminalID, onStateChange, onAttached: saveTabs }));
  entry?.session.connect();
}

// openRun opens a tab that runs one command and previews what it serves.
// Preview opens the same tab from the other end: no command, just the port of
// an app already running in the sandbox.
function openRun(sandboxID, { terminalID = null, label = null, command = "", port = null, focus = "command" } = {}) {
  const entry = addTab("run", sandboxID, label, (pane, tabLabel, onStateChange) =>
    new RunSession(sandboxID, tabLabel, pane, { terminalID, command, port, onStateChange, onSave: saveTabs }));
  if (entry) entry.session.focusInput(focus);
}

function activateTab(entry) {
  state.activeTab = entry;
  for (const other of state.tabs) {
    const active = other === entry;
    other.tab.setAttribute("aria-selected", String(active));
    other.tab.setAttribute("tabindex", active ? "0" : "-1");
    other.pane.setAttribute("aria-hidden", String(!active));
    other.pane.classList.toggle("active", active);
  }
  $("#workspace-empty").hidden = true;
  updateConsoleUI();
  entry.session.focus();
}

// closeTab closes the tab and ends whatever it was running.
function closeTab(entry) {
  entry.session.dispose();
  entry.tab.remove();
  entry.pane.remove();
  const index = state.tabs.indexOf(entry);
  state.tabs.splice(index, 1);
  saveTabs();
  if (state.activeTab === entry) {
    const next = state.tabs[Math.min(index, state.tabs.length - 1)];
    if (next) activateTab(next);
    else {
      state.activeTab = null;
      $("#workspace-empty").hidden = false;
    }
  }
  updateConsoleUI();
}

// ---------- settings ----------

function setupSettings() {
  const dialog = $("#settings");
  const input = $("#api-key");
  const sections = [
    ["settings-api-open", "settings-form"],
    ["cloud-storage-open", "cloud-storage-settings"],
    ["images-open", "images-settings"],
  ];
  const show = (open) => {
    for (const [button, section] of sections) {
      $(`#${section}`).hidden = button !== open;
      $(`#${button}`).setAttribute("aria-pressed", String(button === open));
    }
  };
  const showAPI = () => show("settings-api-open");
  $("#image-select").addEventListener("change", showImageHint);
  $("#settings-open").addEventListener("click", () => {
    showAPI();
    dialog.returnValue = "";
    input.value = getAPIKey();
    dialog.showModal();
  });
  $("#settings-close").addEventListener("click", () => dialog.close("cancel"));
  $("#settings-api-open").addEventListener("click", showAPI);
  $("#images-open").addEventListener("click", () => { show("images-open"); void renderImages(); });
  setupCloudStorage({ toast, onChange: () => refreshSnapshots(true) });
  dialog.addEventListener("close", () => {
    if (dialog.returnValue !== "save") return;
    setAPIKey(input.value.trim());
    toast(input.value.trim() ? "API key saved" : "API key cleared");
    loadImages();
    refreshSandboxes();
    refreshSnapshots(true);
  });
}

// ---------- start ----------
const workspaceTools = setupWorkspaceTools({ toast, onPolicyChange: refreshSandboxes });
const fileExplorer = new FileExplorer({ toast });
const consoleUI = new ConsoleUI({ onFiles: (sb) => fileExplorer.open(sb), onRun: openRun, onTerminal: openTerminal });

function start() {
  setupSchedulePickers();
  setupSettings();
  $("#create-form").addEventListener("submit", createSandbox);
  refreshEngineStatus();
  loadImages();
  refreshSandboxes();
  refreshSnapshots();
  for (const saved of savedTabs()) {
    if (saved.kind === "run") openRun(saved.sandboxID, saved);
    else openTerminal(saved.sandboxID, saved);
  }
  void refreshCapacity();
  setInterval(() => {
    if (document.hidden) return;
    refreshSandboxes();
    refreshCapacity();
    refreshEngineStatus();
    refreshSnapshots();
  }, POLL_INTERVAL_MS);
  setInterval(refreshSnapshotJobs, JOBS_INTERVAL_MS);
}

start();
