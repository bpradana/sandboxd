import { api } from "./api.js";

const $ = (selector) => document.querySelector(selector);

export function setupWorkspaceTools({ toast, onPolicyChange }) {
  let policySandbox = null;

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

  return { openBackupPolicy };
}
