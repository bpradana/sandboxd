import test from "node:test";
import assert from "node:assert/strict";
import { parseHTML } from "linkedom";
import { confirmAction, promptValue } from "./dist/dialogs.js";

function fixture() {
  const { document, window } = parseHTML("<!doctype html><html><body><button id='origin'>Files</button><dialog id='files' open></dialog></body></html>");
  let focused = document.getElementById("origin");
  Object.defineProperty(document, "activeElement", { get: () => focused });
  window.HTMLElement.prototype.focus = function () { focused = this; };
  const make = document.createElement.bind(document);
  document.createElement = (tag) => {
    const node = make(tag);
    if (tag === "dialog") {
      Object.defineProperty(node, "open", { get() { return this.hasAttribute("open"); } });
      node.showModal = () => node.setAttribute("open", "");
      node.close = () => { node.removeAttribute("open"); node.dispatchEvent(new window.Event("close")); };
    }
    if (tag === "input") {
      node.select = () => {};
      node.reportValidity = () => !!node.value && (!node.pattern || new RegExp(`^(?:${node.pattern})$`).test(node.value));
    }
    return node;
  };
  const dialog = () => document.querySelector(".decision-dialog");
  const submit = () => dialog().querySelector("form").dispatchEvent(new window.Event("submit", { bubbles: true, cancelable: true }));
  const cancel = () => dialog().querySelector("button").click();
  return { document, window, dialog, submit, cancel, options: { document } };
}

test("confirmations render safely above an existing modal and default focus to Cancel", async () => {
  const f = fixture();
  const answer = confirmAction("Delete <img src=x onerror=alert(1)>?", { ...f.options, title: "Delete file?", confirmLabel: "Delete file" });
  assert.equal(f.dialog().querySelector("h2").textContent, "Delete file?");
  assert.match(f.dialog().textContent, /<img src=x/);
  assert.equal(f.dialog().querySelector("img"), null);
  assert.equal(f.document.activeElement.textContent, "Cancel");
  assert.equal(f.dialog().dataset.danger, "true");
  f.submit();
  assert.equal(await answer, true);
  assert.equal(f.dialog(), null);
  assert.equal(f.document.getElementById("files").hasAttribute("open"), true);
  assert.equal(f.document.activeElement.id, "origin");
});

test("Cancel and Escape both refuse the action", async () => {
  const f = fixture();
  const first = confirmAction("Kill this sandbox?", f.options);
  f.cancel(); assert.equal(await first, false);
  const second = confirmAction("Delete this backup?", f.options);
  const event = new f.window.Event("cancel", { cancelable: true });
  f.dialog().dispatchEvent(event);
  assert.equal(event.defaultPrevented, true);
  assert.equal(await second, false);
});

test("image naming validates inline and returns typed input or null on cancel", async () => {
  const f = fixture();
  const options = { ...f.options, title: "Create a base image", confirmLabel: "Create image", input: { label: "Image name", pattern: "[a-z0-9][a-z0-9._\\-]{0,63}", maxLength: 64 } };
  const result = promptValue("Choose a name", options);
  const input = f.dialog().querySelector("input");
  assert.equal(f.document.activeElement, input);
  input.value = "Invalid Name"; f.submit();
  assert.ok(f.dialog(), "invalid input must not close the dialog");
  input.value = "python-ready"; f.submit();
  assert.equal(await result, "python-ready");
  const canceled = promptValue("Choose a name", options); f.cancel();
  assert.equal(await canceled, null);
});

test("queued decisions never replace the visible decision and can be aborted", async () => {
  const f = fixture();
  const controller = new AbortController();
  const first = confirmAction("First action", f.options);
  const second = confirmAction("Second action", { ...f.options, signal: controller.signal });
  assert.equal(f.document.querySelectorAll(".decision-dialog").length, 1);
  controller.abort();
  assert.equal(await second, false);
  assert.match(f.dialog().textContent, /First action/);
  f.submit(); assert.equal(await first, true);
  await Promise.resolve();
  const third = confirmAction("Third action", f.options);
  const fourth = confirmAction("Fourth action", f.options);
  f.cancel(); assert.equal(await third, false);
  await Promise.resolve();
  assert.match(f.dialog().textContent, /Fourth action/);
  f.submit(); assert.equal(await fourth, true);
});

test("aborting the sandbox request closes its decision and releases the queue", async () => {
  const f = fixture();
  const controller = new AbortController();
  const result = confirmAction("Overwrite this file?", { ...f.options, signal: controller.signal });
  controller.abort();
  assert.equal(await result, false);
  assert.equal(f.dialog(), null);
  const next = confirmAction("Another action", f.options);
  f.cancel(); assert.equal(await next, false);
  const alreadyAborted = confirmAction("Never show", { ...f.options, signal: controller.signal });
  assert.equal(await alreadyAborted, false);
  assert.equal(f.dialog(), null);
});
