import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { parseHTML } from "linkedom";
import { refreshSchedule, setupSchedulePickers } from "./dist/schedule-picker.js";
import { setupWorkspaceTools } from "./dist/workspace-tools.js";

const html = fs.readFileSync(new URL("./dist/index.html", import.meta.url), "utf8");
function fixture() {
  const { window, document } = parseHTML(html);
  // Linkedom does not implement the browser's writable select value or add().
  for (const select of document.querySelectorAll("select[data-schedule]")) {
    let value = select.options[0]?.value;
    Object.defineProperty(select, "value", {
      get() { return value; },
      set(next) { value = [...this.options].some((option) => option.value === next) ? next : ""; },
    });
    select.add = (option) => select.append(option);
  }
  setupSchedulePickers(document);
  const select = document.getElementById("create-backup-interval");
  const picker = refreshSchedule(select);
  const key = (value) => {
    const event = new window.Event("keydown", { bubbles: true, cancelable: true });
    Object.defineProperty(event, "key", { value }); picker.button.dispatchEvent(event); return event;
  };
  return { window, document, select, picker, key };
}

test("the schedule menu keeps native form values and emits change only on selection", () => {
  const f = fixture();
  const { picker, select } = f;
  assert.equal(select.hidden, true);
  assert.equal(select.name, "backup_interval");
  assert.equal(picker.button.type, "button");
  assert.equal(picker.button.textContent, "Off");
  assert.equal(select.parentElement.querySelector("label").getAttribute("for"), picker.button.id);
  let changes = 0; select.addEventListener("change", () => changes++);
  picker.button.click();
  assert.equal(picker.button.getAttribute("aria-expanded"), "true");
  assert.equal(picker.list.children.length, 4);
  picker.list.children[1].click();
  assert.equal(select.value, "900");
  assert.equal(picker.button.textContent, "Every 15 minutes");
  assert.equal(picker.list.hidden, true);
  assert.equal(changes, 1);
  picker.show(); picker.list.children[1].click();
  assert.equal(changes, 1);
});

test("keyboard selection, Escape, Tab and type-ahead work without submitting the form", () => {
  const f = fixture();
  let escaped = 0;
  f.select.closest("dialog").addEventListener("keydown", (event) => { if (event.key === "Escape") escaped++; });
  assert.equal(f.key("Enter").defaultPrevented, true);
  f.key("ArrowDown");
  assert.equal(f.select.value, "0", "moving through options must not change the form yet");
  assert.match(f.picker.button.getAttribute("aria-activedescendant"), /option-1$/);
  assert.equal(f.key("Escape").defaultPrevented, true);
  assert.equal(escaped, 0, "Escape closes the menu before it can close the dialog");
  assert.equal(f.select.value, "0");
  f.key(" "); f.key("End"); f.key("Enter");
  assert.equal(f.select.value, "86400");
  f.key("ArrowUp"); f.key("Home"); f.key("ArrowDown"); f.key("Tab");
  assert.equal(f.select.value, "900");
  assert.equal(f.picker.open, false);
  f.key("h"); assert.equal(f.select.value, "3600");
});

test("outside clicks and dialog close collapse the menu and disabled options are skipped", () => {
  const f = fixture();
  f.picker.show();
  f.document.body.dispatchEvent(new f.window.Event("pointerdown", { bubbles: true }));
  assert.equal(f.picker.open, false);
  f.picker.show();
  f.select.closest("dialog").dispatchEvent(new f.window.Event("close"));
  assert.equal(f.picker.open, false);
  f.select.options[1].disabled = true;
  f.picker.refresh();
  f.key("Enter"); f.key("ArrowDown"); f.key("Enter");
  assert.equal(f.select.value, "3600");
  f.picker.show(); f.picker.list.children[1].click();
  assert.equal(f.select.value, "3600");
});

test("the policy editor loads and saves custom intervals through the original select", async (t) => {
  const f = fixture();
  const previous = { document: globalThis.document, Option: globalThis.Option, fetch: globalThis.fetch };
  Object.assign(globalThis, { document: f.document, Option: function (label, value) { const option = f.document.createElement("option"); option.textContent = label; option.value = value; return option; } });
  t.after(() => Object.assign(globalThis, previous));
  const dialog = f.document.getElementById("backup-policy-dialog");
  dialog.showModal = () => { dialog.open = true; };
  dialog.close = () => { dialog.open = false; dialog.dispatchEvent(new f.window.Event("close")); };
  let saved, changed = false;
  globalThis.fetch = async (url, options) => {
    saved = { url, body: JSON.parse(options.body) };
    return new Response(JSON.stringify({ data: {} }), { headers: { "Content-Type": "application/json" } });
  };
  const tools = setupWorkspaceTools({ toast() {}, onPolicyChange: async () => { changed = true; } });
  tools.openBackupPolicy({ id: "sbx-policy", snapshot_policy: { interval_seconds: 1200, retain: 3, before_expiry: true } });
  const select = f.document.getElementById("backup-policy-interval");
  const picker = refreshSchedule(select);
  assert.equal(picker.button.textContent, "Every 1200 seconds");
  picker.show();
  picker.list.children[2].click();
  await f.document.getElementById("backup-policy-form").onsubmit({ preventDefault() {} });
  assert.equal(saved.url, "/api/v1/sandboxes/sbx-policy/snapshot-policy");
  assert.deepEqual(saved.body, { interval_seconds: 3600, retain: 3, before_expiry: true });
  assert.equal(changed, true);
  assert.equal(dialog.open, false);
  tools.openBackupPolicy({ id: "sbx-other", snapshot_policy: { interval_seconds: 0, retain: 5 } });
  assert.equal(picker.button.textContent, "Off");
  assert.equal(picker.list.children.length, 4, "custom intervals from the previous sandbox are removed");
});
