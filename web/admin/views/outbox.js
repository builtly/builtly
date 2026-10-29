import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

const PAGE_SIZE = 50;
const CURSOR_FIRST = "_";

function outboxStamp(status) {
  const s = status || "unknown";
  let tone = "unknown";
  if (s === "done") tone = "active";
  else if (s === "pending" || s === "processing") tone = "pending";
  else if (s === "dead") tone = "disabled";
  return `<span class="user-stamp user-stamp--${tone}">${esc(s)}</span>`;
}

function shortRef(id) {
  if (!id) return "—";
  return id.length > 12 ? id.slice(0, 10) : id;
}

function payloadPreview(payload) {
  try {
    return typeof payload === "string" ? payload : JSON.stringify(payload || {});
  } catch {
    return String(payload ?? "");
  }
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

function encodeCursor(c) {
  return c || CURSOR_FIRST;
}

function decodeCursor(c) {
  return !c || c === CURSOR_FIRST ? "" : c;
}

function parseTrail(raw) {
  if (!raw) return [];
  return raw.split("|").filter((p) => p !== "").map(decodeCursor);
}

function outboxHash({ status = "", subscriber = "", cursor = "", trail = [] } = {}) {
  const p = new URLSearchParams();
  if (status) p.set("status", status);
  if (subscriber) p.set("subscriber", subscriber);
  if (cursor) p.set("cursor", cursor);
  if (trail.length) p.set("trail", trail.map(encodeCursor).join("|"));
  const qs = p.toString();
  return "#/outbox" + (qs ? "?" + qs : "");
}

export async function renderOutbox(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const status = params.get("status") || "";
  const subscriber = params.get("subscriber") || "";
  const cursor = params.get("cursor") || "";
  const trail = parseTrail(params.get("trail") || "");
  const page = trail.length + 1;
  el.innerHTML = `<div class="page-head"><h1>Outbox</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: String(PAGE_SIZE) });
  if (status) qs.set("status", status);
  if (subscriber) qs.set("subscriber", subscriber);
  if (cursor) qs.set("cursor", cursor);
  const data = await api("GET", "/outbox?" + qs.toString());
  const items = data.items || [];
  const total = Number(data.total ?? items.length);
  const nextCursor = data.next_cursor || "";

  const rows = items.map((r, i) => {
    const err = r.last_error || "";
    const payload = payloadPreview(r.payload);
    const retry = r.status === "dead"
      ? `<button type="button" class="ob-retry retry" data-id="${esc(r.id)}">Retry</button>`
      : `<span class="user-empty">—</span>`;
    return `<tr class="ob-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String((page - 1) * PAGE_SIZE + i + 1).padStart(2, "0")}</td>
      <td class="user-when">${esc(fmtTime(r.created_at))}</td>
      <td>
        <div class="ob-event">
          <span class="ob-event__type mono">${esc(r.event_type)}</span>
          <span class="ob-event__id mono" title="${esc(r.id)}">${esc(shortRef(r.id))}</span>
        </div>
      </td>
      <td class="ob-sub">${esc(r.subscriber || "—")}</td>
      <td>${outboxStamp(r.status)}</td>
      <td class="ob-num">${esc(r.attempts)}</td>
      <td class="ob-err" title="${esc(err)}">${err ? esc(err) : `<span class="user-empty">—</span>`}</td>
      <td class="ob-payload mono" title="${esc(payload)}">${esc(payload)}</td>
      <td class="ob-actions">${retry}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="9" class="empty user-empty-row">No outbox rows.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">Platform · delivery</p>
        <h1 class="user-page-title">Outbox</h1>
        <p class="muted">Async event delivery queue — retry dead letters from here.</p>
      </div>
      <div class="user-count" aria-label="Outbox rows">${total}</div>
    </div>

    <form id="filters" class="toolbar user-search ops-filters">
      <label class="user-search__label" for="ob-subscriber">Filter</label>
      <div class="toolbar-fields ops-filters__fields">
        <div class="field field--choice ops-filters__select">
          <select name="status" id="ob-status" aria-label="Status">
            <option value="">Any status</option>
            <option value="pending"${status === "pending" ? " selected" : ""}>pending</option>
            <option value="processing"${status === "processing" ? " selected" : ""}>processing</option>
            <option value="done"${status === "done" ? " selected" : ""}>done</option>
            <option value="dead"${status === "dead" ? " selected" : ""}>dead</option>
          </select>
        </div>
        <input id="ob-subscriber" name="subscriber" value="${esc(subscriber)}" placeholder="Subscriber" autocomplete="off">
      </div>
      <button type="submit">Filter</button>
    </form>

    <div class="table-wrap apps-table-wrap ob-table-wrap">
      <table class="apps-table ob-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>When</th><th>Event</th><th>Subscriber</th><th>Status</th>
          <th>Tries</th><th>Error</th><th>Payload</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    <nav class="user-pager" aria-label="Outbox pagination">
      <button type="button" class="user-pager__btn" id="ob-prev" ${trail.length ? "" : "disabled"}>Prev</button>
      <span class="user-pager__meta">Page ${page} · ${items.length} shown</span>
      <button type="button" class="user-pager__btn" id="ob-next" ${nextCursor ? "" : "disabled"}>Next</button>
    </nav>`;

  wireMobileSelect(el.querySelector("#ob-status"), { title: "Status" });
  applyRevealDelays(el, ".ob-row[data-reveal]");

  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    location.hash = outboxHash({
      status: String(fd.get("status") || ""),
      subscriber: String(fd.get("subscriber") || "").trim(),
    });
  });

  el.querySelector("#ob-prev").addEventListener("click", () => {
    if (!trail.length) return;
    const prev = trail[trail.length - 1] || "";
    location.hash = outboxHash({ status, subscriber, cursor: prev, trail: trail.slice(0, -1) });
  });

  el.querySelector("#ob-next").addEventListener("click", () => {
    if (!nextCursor) return;
    location.hash = outboxHash({ status, subscriber, cursor: nextCursor, trail: [...trail, cursor] });
  });

  el.querySelectorAll(".retry").forEach((btn) => {
    btn.addEventListener("click", async () => {
      await api("POST", "/outbox/" + encodeURIComponent(btn.dataset.id) + "/retry");
      showOk("Queued for retry");
      await renderOutbox(el);
    });
  });
}
