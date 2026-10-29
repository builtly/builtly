/**
 * Global toast host. Uses the Popover API when available so toasts stack in the
 * browser top layer above native <dialog> modals.
 */

const HOST_ID = "toast-host";

/** @type {WeakMap<HTMLElement, number>} */
const timers = new WeakMap();

function ensureHost() {
  let host = document.getElementById(HOST_ID);
  if (host) return host;
  host = document.createElement("div");
  host.id = HOST_ID;
  host.className = "toast-host";
  host.setAttribute("popover", "manual");
  host.setAttribute("aria-live", "polite");
  host.setAttribute("aria-relevant", "additions");
  document.body.appendChild(host);
  return host;
}

function revealHost(host) {
  if (typeof host.showPopover !== "function") return;
  try {
    if (!host.matches(":popover-open")) host.showPopover();
  } catch {
    /* ignore unsupported / already open races */
  }
}

function hideHostIfEmpty(host) {
  if (host.childElementCount > 0) return;
  if (typeof host.hidePopover !== "function") return;
  try {
    if (host.matches(":popover-open")) host.hidePopover();
  } catch {
    /* ignore */
  }
}

function clearTimer(el) {
  const id = timers.get(el);
  if (id) {
    window.clearTimeout(id);
    timers.delete(el);
  }
}

/**
 * @param {HTMLElement} el
 * @param {number} ms
 */
function armDismiss(el, ms) {
  clearTimer(el);
  if (!(ms > 0)) return;
  const id = window.setTimeout(() => dismissToast(el), ms);
  timers.set(el, id);
}

/**
 * @param {HTMLElement} el
 */
export function dismissToast(el) {
  if (!el || !el.isConnected || el.dataset.dismissing === "1") return;
  el.dataset.dismissing = "1";
  clearTimer(el);
  el.classList.add("toast--out");
  let finished = false;
  const done = () => {
    if (finished) return;
    finished = true;
    const host = el.parentElement;
    el.remove();
    if (host) hideHostIfEmpty(host);
  };
  el.addEventListener("transitionend", done, { once: true });
  window.setTimeout(done, 280);
}

/**
 * @param {{
 *   tone?: "ok" | "err",
 *   message: string,
 *   detail?: string,
 *   code?: string,
 *   duration?: number,
 * }} opts
 */
export function showToast(opts = {}) {
  const tone = opts.tone === "ok" ? "ok" : "err";
  const message = String(opts.message || "").trim() || (tone === "ok" ? "Done" : "Something went wrong");
  const detail = String(opts.detail || "").trim();
  const code = String(opts.code || "").trim();
  const duration = opts.duration ?? (tone === "ok" ? 3200 : 6500);

  const host = ensureHost();
  revealHost(host);

  const el = document.createElement("div");
  el.className = `toast toast--${tone}`;
  el.setAttribute("role", tone === "err" ? "alert" : "status");

  const body = document.createElement("div");
  body.className = "toast__body";

  const msg = document.createElement("p");
  msg.className = "toast__msg";
  msg.textContent = message;
  body.appendChild(msg);

  if (detail) {
    const d = document.createElement("p");
    d.className = "toast__detail";
    d.textContent = detail;
    body.appendChild(d);
  }

  if (code) {
    const c = document.createElement("p");
    c.className = "toast__code";
    c.textContent = code;
    body.appendChild(c);
  }

  const close = document.createElement("button");
  close.type = "button";
  close.className = "toast__close";
  close.setAttribute("aria-label", "Dismiss");
  close.textContent = "✕";
  close.addEventListener("click", () => dismissToast(el));

  el.appendChild(body);
  el.appendChild(close);

  el.addEventListener("mouseenter", () => clearTimer(el));
  el.addEventListener("mouseleave", () => armDismiss(el, duration));
  el.addEventListener("focusin", () => clearTimer(el));
  el.addEventListener("focusout", () => armDismiss(el, duration));

  host.appendChild(el);
  // Enter animation: next frame after insert.
  requestAnimationFrame(() => el.classList.add("toast--in"));
  armDismiss(el, duration);
  return el;
}

/**
 * Pick a short human detail from apperr.details without dumping JSON.
 * @param {unknown} details
 */
function detailFrom(details) {
  if (details == null) return "";
  if (typeof details === "string") return details;
  if (typeof details !== "object") return String(details);
  const o = /** @type {Record<string, unknown>} */ (details);
  if (typeof o.hint === "string" && o.hint.trim()) return o.hint.trim();
  if (typeof o.reason === "string" && o.reason.trim()) return o.reason.trim();
  if (typeof o.provider === "string" && o.provider.trim()) {
    return `Provider: ${o.provider.trim()}`;
  }
  return "";
}

/**
 * @param {{ code?: string, message?: string, details?: unknown }|null|undefined} err
 */
export function showErrorToast(err) {
  if (!err) return;
  showToast({
    tone: "err",
    message: err.message || "request failed",
    detail: detailFrom(err.details),
    code: err.code || "",
  });
}

/** @param {string} msg */
export function showOkToast(msg) {
  showToast({ tone: "ok", message: msg || "Done" });
}
