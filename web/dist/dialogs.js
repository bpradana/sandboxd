// Non-blocking, themed decisions shared by the console and nested file editor.
const queues = new WeakMap();

class DialogQueue {
  constructor(doc) { this.doc = doc; this.pending = []; this.active = null; }

  ask(options) {
    return new Promise((resolve) => {
      const item = { ...options, resolve };
      item.cancel = () => {
        if (this.active === item) this.finish(item, null);
        else { this.pending = this.pending.filter((entry) => entry !== item); this.resolve(item, null); }
      };
      if (item.signal?.aborted) { resolve(null); return; }
      item.signal?.addEventListener("abort", item.cancel, { once: true });
      this.pending.push(item);
      this.next();
    });
  }

  resolve(item, result) {
    item.signal?.removeEventListener("abort", item.cancel);
    item.resolve(result);
  }

  next() {
    if (this.active || !this.pending.length) return;
    const item = this.pending.shift();
    this.active = item;
    item.previousFocus = this.doc.activeElement;
    const make = (tag, className, text) => {
      const node = this.doc.createElement(tag);
      if (className) node.className = className;
      if (text !== undefined) node.textContent = text;
      return node;
    };
    const dialog = make("dialog", "decision-dialog");
    item.dialog = dialog;
    dialog.dataset.danger = String(item.danger);
    dialog.setAttribute("aria-labelledby", "decision-title");
    dialog.setAttribute("aria-describedby", "decision-message");
    const heading = make("h2", "", item.title); heading.id = "decision-title";
    const message = make("p", "decision-message muted", item.message); message.id = "decision-message";
    const form = make("form", "decision-form");
    form.append(heading, message);
    let input;
    if (item.input) {
      const label = make("label", "", item.input.label);
      input = make("input");
      input.type = "text";
      input.value = item.input.value || "";
      input.required = true;
      input.autocomplete = "off";
      input.spellcheck = false;
      if (item.input.pattern) input.pattern = item.input.pattern;
      if (item.input.maxLength) input.maxLength = item.input.maxLength;
      if (item.input.placeholder) input.placeholder = item.input.placeholder;
      label.append(input); form.append(label);
    }
    const actions = make("div", "dialog-actions");
    const cancel = make("button", "btn ghost", "Cancel"); cancel.type = "button";
    const submit = make("button", "btn primary decision-submit", item.confirmLabel); submit.type = "submit";
    actions.append(cancel, submit); form.append(actions); dialog.append(form);
    cancel.onclick = () => this.finish(item, null);
    form.onsubmit = (event) => {
      event.preventDefault();
      if (!input || input.reportValidity()) this.finish(item, input ? input.value : true);
    };
    dialog.addEventListener("cancel", (event) => { event.preventDefault(); this.finish(item, null); });
    dialog.addEventListener("close", () => this.finish(item, null));
    this.doc.body.append(dialog);
    try {
      dialog.showModal();
      // Destructive decisions default to Cancel; naming a resource starts typing.
      (input || cancel).focus();
      input?.select();
    } catch { this.finish(item, null); }
  }

  finish(item, result) {
    if (this.active !== item) return;
    this.active = null;
    if (item.dialog.open) item.dialog.close();
    item.dialog.remove();
    if (item.previousFocus?.isConnected) item.previousFocus.focus({ preventScroll: true });
    this.resolve(item, result);
    queueMicrotask(() => this.next());
  }
}

function ask(options) {
  const doc = options.document || document;
  if (!queues.has(doc)) queues.set(doc, new DialogQueue(doc));
  return queues.get(doc).ask(options);
}

export async function confirmAction(message, options = {}) {
  return (await ask({ title: "Confirm action", confirmLabel: "Continue", danger: true, ...options, message })) === true;
}

export function promptValue(message, options = {}) {
  return ask({ title: "Choose a name", confirmLabel: "Create", danger: false, ...options, message });
}
