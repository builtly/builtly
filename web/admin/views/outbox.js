import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

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

export async function renderOutbox(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const status = params.get("status") || "";
  const subscriber = params.get("subscriber") || "";
  el.innerHTML = `<div class="page-head"><h1>Outbox</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: "100" });
  if (status) qs.set("status", status);
  if (subscriber) qs.set("subscriber", subscriber);
  const data = await api("GET", "/outbox?" + qs.toString());
  const items = data.items || [];
  const deadN = items.filter((r) => r.status === "dead").length;

  const rows = items.map((r, i) => {
    const err = r.last_error || "";
    const payload = payloadPreview(r.payload);
    const retry = r.status === "dead"
      ? `<button type="button" class="ob-retry retry" data-id="${esc(r.id)}">Retry</button>`
      : `<span class="user-empty">—</span>`;
    return `<tr class="ob-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
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
      <div class="user-count" aria-label="Outbox rows">${items.length}${deadN ? `<span class="ov-bound-total"> · ${deadN} dead</span>` : ""}</div>
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
    </div>`;

  wireMobileSelect(el.querySelector("#ob-status"), { title: "Status" });
  applyRevealDelays(el, ".ob-row[data-reveal]");

  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    const q = new URLSearchParams();
    const st = String(fd.get("status") || "");
    const sub = String(fd.get("subscriber") || "").trim();
    if (st) q.set("status", st);
    if (sub) q.set("subscriber", sub);
    const qs2 = q.toString();
    location.hash = "#/outbox" + (qs2 ? "?" + qs2 : "");
  });

  el.querySelectorAll(".retry").forEach((btn) => {
    btn.addEventListener("click", async () => {
      await api("POST", "/outbox/" + encodeURIComponent(btn.dataset.id) + "/retry");
      showOk("Queued for retry");
      await renderOutbox(el);
    });
  });
}
