import { esc } from "./api.js";

export const mobileMQ = window.matchMedia("(max-width: 800px)");

/** @type {AbortController|null} */
let sheetAbort = null;

function layoutEl() {
  return document.querySelector(".layout");
}

function syncChromeLock() {
  const layout = layoutEl();
  if (!layout) {
    document.documentElement.classList.remove("nav-lock");
    return;
  }
  const locked = layout.classList.contains("layout--nav-open")
    || layout.classList.contains("layout--choice-sheet-open");
  document.documentElement.classList.toggle("nav-lock", locked);
}

function closeOtherOverlays(layout) {
  layout.classList.remove("layout--nav-open");
  const navBackdrop = document.getElementById("sidebar-backdrop");
  const openBtn = document.getElementById("sidebar-open");
  const sidebar = document.getElementById("sidebar");
  if (navBackdrop) navBackdrop.hidden = true;
  openBtn?.setAttribute("aria-expanded", "false");
  if (sidebar && mobileMQ.matches) {
    sidebar.inert = true;
    sidebar.setAttribute("aria-hidden", "true");
  }
}

function ensureSheet() {
  const layout = layoutEl();
  if (!layout) return null;
  let sheet = document.getElementById("choice-sheet");
  if (sheet) return sheet;

  layout.insertAdjacentHTML("beforeend", `
    <div class="choice-sheet-backdrop" id="choice-sheet-backdrop" hidden></div>
    <div class="choice-sheet" id="choice-sheet" role="dialog" aria-modal="true"
      aria-labelledby="choice-sheet-title" hidden>
      <div class="choice-sheet__head">
        <h2 class="choice-sheet__title" id="choice-sheet-title">Choose</h2>
        <button type="button" class="choice-sheet__close" id="choice-sheet-close" aria-label="Close">✕</button>
      </div>
      <div class="choice-sheet__list" id="choice-sheet-list" role="listbox"></div>
      <div class="choice-sheet__footer" id="choice-sheet-footer" hidden></div>
    </div>`);
  return document.getElementById("choice-sheet");
}

function selectedLabel(select) {
  const opt = select.selectedOptions?.[0];
  return (opt?.textContent || select.value || "—").trim();
}

function syncTrigger(select, trigger) {
  trigger.textContent = selectedLabel(select);
  trigger.disabled = select.disabled;
}

/**
 * Close the shared mobile choice sheet (if open).
 */
export function closeChoiceSheet() {
  const layout = layoutEl();
  const sheet = document.getElementById("choice-sheet");
  const backdrop = document.getElementById("choice-sheet-backdrop");
  const footer = document.getElementById("choice-sheet-footer");
  const appChip = document.getElementById("mobile-app-chip");
  if (layout) layout.classList.remove("layout--choice-sheet-open");
  if (sheet) sheet.hidden = true;
  if (backdrop) backdrop.hidden = true;
  if (footer) {
    footer.hidden = true;
    footer.innerHTML = "";
  }
  appChip?.setAttribute("aria-expanded", "false");
  if (sheetAbort) {
    sheetAbort.abort();
    sheetAbort = null;
  }
  syncChromeLock();
}

/**
 * @typedef {{ value: string, label: string, sub?: string, disabled?: boolean }} ChoiceItem
 */

/**
 * Open the shared bottom choice sheet.
 * @param {{
 *   title?: string,
 *   items: ChoiceItem[],
 *   value?: string,
 *   footerHTML?: string,
 *   onSelect?: (value: string, item: ChoiceItem) => void,
 * }} opts
 */
export function openChoicePicker(opts = {}) {
  if (!mobileMQ.matches) return;
  const layout = layoutEl();
  const sheet = ensureSheet();
  if (!layout || !sheet) return;

  closeOtherOverlays(layout);
  if (sheetAbort) sheetAbort.abort();
  sheetAbort = new AbortController();
  const { signal } = sheetAbort;

  const items = opts.items || [];
  const titleEl = document.getElementById("choice-sheet-title");
  const list = document.getElementById("choice-sheet-list");
  const footer = document.getElementById("choice-sheet-footer");
  const backdrop = document.getElementById("choice-sheet-backdrop");
  const closeBtn = document.getElementById("choice-sheet-close");
  if (titleEl) titleEl.textContent = opts.title || "Choose";
  if (!list || !backdrop || !closeBtn || !footer) return;

  list.innerHTML = items.map((item, i) => {
    const on = item.value === opts.value;
    const sub = item.sub
      ? `<span class="choice-sheet__item-sub">${esc(item.sub)}</span>`
      : "";
    return `<button type="button" class="choice-sheet__item${on ? " choice-sheet__item--current" : ""}"
        data-index="${i}" role="option" aria-selected="${on ? "true" : "false"}"
        ${item.disabled ? "disabled" : ""}>
        <span class="choice-sheet__item-label">${esc(item.label)}</span>
        ${sub}
      </button>`;
  }).join("") || `<p class="choice-sheet__empty">No options</p>`;

  if (opts.footerHTML) {
    footer.innerHTML = opts.footerHTML;
    footer.hidden = false;
  } else {
    footer.innerHTML = "";
    footer.hidden = true;
  }

  const finish = () => closeChoiceSheet();

  list.querySelectorAll(".choice-sheet__item[data-index]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const idx = Number(btn.getAttribute("data-index"));
      const item = items[idx];
      if (!item || item.disabled) return;
      opts.onSelect?.(item.value, item);
      finish();
    }, { signal });
  });

  footer.querySelectorAll("a, button").forEach((el) => {
    el.addEventListener("click", () => finish(), { signal });
  });

  closeBtn.addEventListener("click", finish, { signal });
  backdrop.addEventListener("click", finish, { signal });
  document.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape") finish();
  }, { signal });

  layout.classList.add("layout--choice-sheet-open");
  sheet.hidden = false;
  backdrop.hidden = false;
  syncChromeLock();

  const current = list.querySelector(".choice-sheet__item--current") || list.querySelector(".choice-sheet__item");
  current?.focus();
}

/**
 * Open the app switcher via the shared choice sheet.
 * @param {{
 *   apps: { id: string, name: string, slug: string }[],
 *   currentID?: string,
 *   onSelect: (appID: string) => void,
 * }} opts
 */
export function openAppSwitcher(opts) {
  const apps = opts.apps || [];
  openChoicePicker({
    title: "Switch app",
    value: opts.currentID || "",
    items: apps.map((a) => ({
      value: a.id,
      label: a.name || a.slug || a.id,
      sub: a.slug || "",
    })),
    footerHTML: `<a class="choice-sheet__footer-link" href="#/apps">Manage all apps</a>`,
    onSelect: (id) => opts.onSelect?.(id),
  });
  document.getElementById("mobile-app-chip")?.setAttribute("aria-expanded", "true");
}

/**
 * Enhance a <select> with a mobile bottom-sheet picker.
 * Desktop keeps the native control; phones use a trigger + choice sheet.
 * @param {HTMLSelectElement|null} select
 * @param {{ title?: string }} [opts]
 */
export function wireMobileSelect(select, opts = {}) {
  if (!select || select.dataset.choiceWired === "1") return;
  const field = select.closest(".field") || select.parentElement;
  if (!field) return;

  select.dataset.choiceWired = "1";
  select.classList.add("choice-select");
  field.classList.add("field--choice");

  const trigger = document.createElement("button");
  trigger.type = "button";
  trigger.className = "choice-trigger";
  trigger.setAttribute("aria-haspopup", "dialog");
  trigger.setAttribute("aria-controls", "choice-sheet");
  syncTrigger(select, trigger);
  field.appendChild(trigger);

  trigger.addEventListener("click", () => {
    if (!mobileMQ.matches || select.disabled) return;
    const title = opts.title || field.querySelector("label")?.textContent?.trim() || "Choose";
    openChoicePicker({
      title,
      value: select.value,
      items: Array.from(select.options).map((opt) => ({
        value: opt.value,
        label: (opt.textContent || opt.value).trim(),
        disabled: opt.disabled,
      })),
      onSelect: (value) => {
        if (select.value !== value) {
          select.value = value;
          select.dispatchEvent(new Event("change", { bubbles: true }));
          select.dispatchEvent(new Event("input", { bubbles: true }));
        }
        syncTrigger(select, trigger);
      },
    });
  });

  select.addEventListener("change", () => syncTrigger(select, trigger));
}

/** Recompute scroll lock after chrome overlays change (used by app shell). */
export { syncChromeLock };
