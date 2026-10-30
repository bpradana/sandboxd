import { api } from "./api.js";

const $ = (selector) => document.querySelector(selector);

export function setupWorkspaceTools({ toast, onPolicyChange, onResize }) {
  let policySandbox = null;
  let previewSandbox = null;
  let preview = null;
  let revision = 0;

  function openBackupPolicy(sb) {
    policySandbox = sb.id;
    $("#backup-policy-sandbox").textContent = sb.id;
    const interval = sb.snapshot_policy?.interval_seconds || 0;
    const select = $("#backup-policy-interval");
    for (const option of [...select.options]) if (option.dataset.custom) option.remove();
    if (![...select.options].some((option) => Number(option.value) === interval)) {
      const option = new Option(`Every ${interval} seconds`, String(interval));
      option.dataset.custom = "true";
      select.add(option);
    }
    select.value = String(interval);
    $("#backup-policy-expiry").checked = sb.snapshot_policy?.before_expiry || false;
    $("#backup-policy-retain").value = sb.snapshot_policy?.retain || 5;
    $("#backup-policy-status").textContent = sb.last_snapshot_error || "";
    $("#backup-policy-dialog").showModal();
  }

  $("#backup-policy-cancel").onclick = () => $("#backup-policy-dialog").close();
  $("#backup-policy-form").onsubmit = async (event) => {
    event.preventDefault();
    $("#backup-policy-save").disabled = true;
    try {
      await api("PUT", `/sandboxes/${encodeURIComponent(policySandbox)}/snapshot-policy`, {
        interval_seconds: Number($("#backup-policy-interval").value),
        before_expiry: $("#backup-policy-expiry").checked,
        retain: Number($("#backup-policy-retain").value),
      });
      $("#backup-policy-dialog").close();
      toast("Backup policy saved");
      await onPolicyChange();
    } catch (err) { $("#backup-policy-status").textContent = err.message; }
    finally { $("#backup-policy-save").disabled = false; }
  };

  async function revoke(session, sandboxID) {
    if (!session) return;
    try { await api("DELETE", `/sandboxes/${encodeURIComponent(sandboxID)}/previews`, { id: session.id }); } catch { /* Expired or stopped. */ }
  }

  function clearPreview() {
    revision++;
    const previous = preview;
    const previousSandbox = previewSandbox;
    preview = null;
    $("#preview-frame").src = "about:blank";
    $("#preview-frame").hidden = true;
    $("#preview-external").hidden = true;
    $("#preview-external").removeAttribute("href");
    $("#preview-refresh").disabled = true;
    void revoke(previous, previousSandbox);
  }

  function openAppPreview(sb) {
    if (previewSandbox !== sb.id) clearPreview();
    previewSandbox = sb.id;
    $("#preview-sandbox").textContent = sb.id;
    $("#app-preview-panel").hidden = false;
    $("#preview-status").textContent = preview ? `Port ${preview.port} · expires ${new Date(preview.expires_at).toLocaleTimeString()}` : "Start your app in the terminal, then enter its HTTP port.";
    onResize();
  }

  $("#preview-close").onclick = () => { clearPreview(); previewSandbox = null; $("#app-preview-panel").hidden = true; onResize(); };
  $("#preview-refresh").onclick = () => { if (preview) $("#preview-frame").src = preview.url; };
  $("#preview-form").onsubmit = async (event) => {
    event.preventDefault();
    clearPreview();
    const currentRevision = revision;
    const sandboxID = previewSandbox;
    $("#preview-connect").disabled = true;
    $("#preview-status").textContent = "Opening preview…";
    try {
      const session = await api("POST", `/sandboxes/${encodeURIComponent(sandboxID)}/previews`, { port: Number($("#preview-port").value) });
      if (revision !== currentRevision) { await revoke(session, sandboxID); return; }
      const url = new URL(session.url);
      if (!["http:", "https:"].includes(url.protocol) || url.origin === location.origin) {
        await revoke(session, sandboxID);
        throw new Error("Preview must use a separate HTTP(S) origin.");
      }
      preview = session;
      $("#preview-frame").src = session.url;
      $("#preview-frame").hidden = false;
      $("#preview-external").href = session.url;
      $("#preview-external").hidden = false;
      $("#preview-refresh").disabled = false;
      $("#preview-status").textContent = `Port ${session.port} · expires ${new Date(session.expires_at).toLocaleTimeString()}. If the app is not ready, start it and refresh.`;
    } catch (err) { if (revision === currentRevision) $("#preview-status").textContent = err.message; }
    finally { $("#preview-connect").disabled = false; }
  };

  function updatePreviews(sandboxes) {
    if (!previewSandbox || sandboxes.some((sb) => sb.id === previewSandbox && sb.state === "running")) return;
    clearPreview();
    $("#preview-status").textContent = "This sandbox has stopped. Restore it or create another sandbox to preview an app.";
  }

  return { openBackupPolicy, openAppPreview, updatePreviews };
}
