// Keep the native select for form submission; present a compact, themed menu.
const pickers = new WeakMap();

export class SchedulePicker {
  constructor(select) {
    this.select = select;
    this.doc = select.ownerDocument;
    this.open = false;
    this.typed = "";
    const make = (tag, className) => {
      const node = this.doc.createElement(tag); node.className = className; return node;
    };
    this.host = make("div", "schedule-picker");
    this.button = make("button", "schedule-trigger");
    this.button.type = "button";
    this.button.id = `${select.id}-trigger`;
    this.button.setAttribute("role", "combobox");
    this.button.setAttribute("aria-haspopup", "listbox");
    this.button.setAttribute("aria-expanded", "false");
    this.list = make("ul", "schedule-menu");
    this.list.id = `${select.id}-menu`;
    this.list.setAttribute("role", "listbox");
    this.list.setAttribute("aria-label", "Backup schedule");
    this.list.hidden = true;
    this.button.setAttribute("aria-controls", this.list.id);
    const label = select.parentElement.querySelector("label");
    if (label) label.setAttribute("for", this.button.id);
    else this.button.setAttribute("aria-label", "Backup schedule");
    this.host.append(this.button, this.list);
    select.after(this.host);
    this.button.onclick = () => this.open ? this.close() : this.show();
    this.button.onkeydown = (event) => this.keydown(event);
    this.doc.addEventListener("pointerdown", (event) => { if (!this.host.contains(event.target)) this.close(); });
    select.addEventListener("change", () => this.refresh());
    select.closest("form")?.addEventListener("reset", () => queueMicrotask(() => this.refresh()));
    select.closest("dialog")?.addEventListener("close", () => this.close());
    select.closest("details")?.addEventListener("toggle", (event) => { if (!event.currentTarget.open) this.close(); });
    this.refresh();
    select.hidden = true;
  }

  refresh() {
    this.options = [...this.select.options];
    this.index = Math.max(0, this.options.findIndex((option) => option.value === this.select.value));
    this.button.textContent = this.options[this.index]?.textContent || "Choose schedule";
    this.button.disabled = this.select.disabled;
    this.list.replaceChildren(...this.options.map((option, index) => {
      const node = this.doc.createElement("li");
      node.id = `${this.select.id}-option-${index}`;
      node.setAttribute("role", "option");
      node.setAttribute("aria-selected", String(option.value === this.select.value));
      node.setAttribute("aria-disabled", String(option.disabled));
      node.textContent = option.textContent;
      node.onclick = () => { if (!option.disabled) this.choose(index); };
      return node;
    }));
    this.highlight();
  }

  show() {
    if (this.button.disabled) return;
    this.refresh();
    this.open = true;
    this.list.hidden = false;
    this.button.setAttribute("aria-expanded", "true");
    this.highlight();
  }

  close() {
    this.open = false;
    this.typed = "";
    this.list.hidden = true;
    this.button.setAttribute("aria-expanded", "false");
    this.button.removeAttribute("aria-activedescendant");
  }

  highlight() {
    for (const [index, node] of [...this.list.children].entries()) node.classList.toggle("active", index === this.index);
    if (this.open && this.list.children[this.index]) {
      this.button.setAttribute("aria-activedescendant", this.list.children[this.index].id);
      this.list.children[this.index].scrollIntoView?.({ block: "nearest" });
    }
  }

  choose(index, focus = true) {
    const option = this.options[index];
    if (!option || option.disabled || this.select.disabled) return;
    const changed = this.select.value !== option.value;
    this.select.value = option.value;
    this.close();
    this.refresh();
    if (changed) {
      const Event = this.doc.defaultView.Event;
      this.select.dispatchEvent(new Event("input", { bubbles: true }));
      this.select.dispatchEvent(new Event("change", { bubbles: true }));
    }
    if (focus) this.button.focus();
  }

  keydown(event) {
    if (event.ctrlKey || event.metaKey || event.altKey || this.button.disabled) return;
    const enabled = this.options.map((option, index) => option.disabled ? -1 : index).filter((index) => index >= 0);
    const position = enabled.indexOf(this.index);
    if (event.key === "Escape" && this.open) { event.preventDefault(); event.stopPropagation(); this.close(); return; }
    if (event.key === "Tab") { if (this.open) this.choose(this.index, false); return; }
    if (["Enter", " "].includes(event.key)) { event.preventDefault(); this.open ? this.choose(this.index) : this.show(); return; }
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      if (!this.open) { this.show(); return; }
      const next = event.key === "Home" ? 0 : event.key === "End" ? enabled.length - 1 : Math.max(0, Math.min(enabled.length - 1, position + (event.key === "ArrowDown" ? 1 : -1)));
      this.index = enabled[next] ?? this.index;
      this.highlight();
      return;
    }
    if (event.key.length === 1) {
      event.preventDefault();
      const key = event.key.toLocaleLowerCase();
      this.typed = Date.now() - this.typedAt > 600 || this.typed === key ? key : this.typed + key;
      this.typedAt = Date.now();
      const match = enabled.find((index) => this.options[index].textContent.toLocaleLowerCase().startsWith(this.typed));
      if (match !== undefined) { this.index = match; this.open ? this.highlight() : this.choose(match); }
    }
  }
}

export function refreshSchedule(select) {
  if (!pickers.has(select)) pickers.set(select, new SchedulePicker(select));
  const picker = pickers.get(select);
  picker.refresh();
  return picker;
}

export function setupSchedulePickers(doc = document) {
  for (const select of doc.querySelectorAll("select[data-schedule]")) refreshSchedule(select);
}
