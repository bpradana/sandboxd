import { api } from "./api.js";

const $ = (selector) => document.querySelector(selector);

// Only public connection metadata is retained here. Typed credentials live in
// the form until save/close and are never copied into browser storage.
export function setupCloudStorage({ toast, onChange }) {
  const dialog = $("#settings");
  const form = $("#cloud-storage-form");
  let connection = null;
  let busy = false;

  function clearCredentials() {
    for (const id of ["#cloud-access-key", "#cloud-secret-key", "#cloud-session-token"]) $(id).value = "";
  }

  function setBusy(value) {
    busy = value;
    $("#cloud-fields").disabled = value;
    $("#cloud-save").disabled = value;
    $("#cloud-test").disabled = value;
    $("#cloud-disconnect").disabled = value || !connection?.connected;
  }

  function updateAuthentication() {
    const keys = $("#cloud-auth-mode").value === "access_keys";
    $("#cloud-credentials").hidden = !keys;
    $("#cloud-credentials").disabled = !keys;
    $("#cloud-identity-help").hidden = keys;
    const saved = connection?.has_saved_credentials && keys;
    $("#cloud-access-key").required = keys && !saved;
    $("#cloud-secret-key").required = keys && !saved;
    $("#cloud-credentials-help").textContent = saved
      ? "Keys are saved on the server. Leave all credential fields blank to keep them, or enter a new access key and secret key."
      : "Keys are stored encrypted on the server and are never returned to this browser.";
  }

  function render(value) {
    connection = value;
    $("#cloud-auth-mode").value = value.auth_mode || "server_identity";
    $("#cloud-bucket").value = value.bucket || "";
    $("#cloud-region").value = value.region || "us-east-1";
    $("#cloud-endpoint").value = value.endpoint || "";
    $("#cloud-prefix").value = value.prefix ?? "sandboxd/snapshots";
    $("#cloud-path-style").checked = !!value.path_style;
    $("#cloud-connection-status").textContent = value.connected
      ? `Connected to ${value.bucket}${value.source === "environment" ? " using server configuration" : ""}.`
      : "No cloud storage connected.";
    clearCredentials();
    updateAuthentication();
  }

  async function load() {
    if (busy) return;
    setBusy(true);
    try { render(await api("GET", "/settings/cloud-storage")); }
    catch (err) { $("#cloud-connection-status").textContent = `Unable to load connection: ${err.message}`; }
    finally { setBusy(false); }
  }

  function request() {
    if (!form.reportValidity()) return null;
    const body = {
      auth_mode: $("#cloud-auth-mode").value,
      bucket: $("#cloud-bucket").value.trim(),
      region: $("#cloud-region").value.trim(),
      endpoint: $("#cloud-endpoint").value.trim(),
      prefix: $("#cloud-prefix").value.trim(),
      path_style: $("#cloud-path-style").checked,
    };
    if (body.auth_mode === "access_keys") {
      body.access_key_id = $("#cloud-access-key").value;
      body.secret_access_key = $("#cloud-secret-key").value;
      body.session_token = $("#cloud-session-token").value;
      body.use_saved_credentials = !!connection?.has_saved_credentials && !body.access_key_id && !body.secret_access_key && !body.session_token;
      if (!body.use_saved_credentials && (!body.access_key_id || !body.secret_access_key)) {
        $("#cloud-connection-status").textContent = "Enter both an access key and a secret key.";
        return null;
      }
    }
    return body;
  }

  $("#cloud-storage-open").addEventListener("click", () => {
    $("#settings-form").hidden = true;
    $("#cloud-storage-settings").hidden = false;
    $("#settings-api-open").setAttribute("aria-pressed", "false");
    $("#cloud-storage-open").setAttribute("aria-pressed", "true");
    load();
  });
  $("#cloud-auth-mode").addEventListener("change", () => { clearCredentials(); updateAuthentication(); });
  dialog.addEventListener("close", clearCredentials);
  window.addEventListener("pagehide", clearCredentials);
  $("#settings-api-open").addEventListener("click", clearCredentials);

  $("#cloud-test").addEventListener("click", async () => {
    if (busy) return;
    const body = request();
    if (!body) return;
    setBusy(true);
    $("#cloud-connection-status").textContent = "Testing cloud access…";
    try {
      await api("POST", "/settings/cloud-storage/test", body);
      $("#cloud-connection-status").textContent = "Connection test passed. Save connection to use this bucket.";
    } catch (err) { $("#cloud-connection-status").textContent = err.message; }
    finally { setBusy(false); }
  });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (busy) return;
    const body = request();
    if (!body) return;
    setBusy(true);
    $("#cloud-connection-status").textContent = "Testing and saving connection…";
    try {
      render(await api("PUT", "/settings/cloud-storage", body));
      toast("Cloud storage connected");
      await onChange();
    } catch (err) { $("#cloud-connection-status").textContent = err.message; }
    finally { setBusy(false); }
  });

  $("#cloud-disconnect").addEventListener("click", async () => {
    if (busy || !window.confirm("Disconnect cloud storage? Existing backups stay in the bucket. Saved credentials will be removed from this deployment.")) return;
    setBusy(true);
    try {
      await api("DELETE", "/settings/cloud-storage");
      render({ ...connection, connected: false, has_saved_credentials: false, auth_mode: "server_identity", source: "saved" });
      toast("Cloud storage disconnected. Backups retained.");
      await onChange();
    } catch (err) { $("#cloud-connection-status").textContent = err.message; }
    finally { setBusy(false); }
  });
}
