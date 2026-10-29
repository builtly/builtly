import { api, esc, fmtTime } from "../api.js";

const PAGE_SIZE = 50;
const CURSOR_FIRST = "_";

function shortRef(id) {
  if (!id) return "—";
  return id.length > 12 ? id.slice(0, 10) : id;
}

function diffPreview(diff) {
  try {
    return typeof diff === "string" ? diff : JSON.stringify(diff ?? "");
  } catch {
    return String(diff ?? "");
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

function auditHash({ appID = "", action = "", cursor = "", trail = [] } = {}) {
  const p = new URLSearchParams();
  if (appID) p.set("app_id", appID);
  if (action) p.set("action", action);
  if (cursor) p.set("cursor", cursor);
  if (trail.length) p.set("trail", trail.map(encodeCursor).join("|"));
  const qs = p.toString();
  return "#/audit" + (qs ? "?" + qs : "");
}

export async function renderAudit(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const appID = params.get("app_id") || "";
  const action = params.get("action") || "";
  const cursor = params.get("cursor") || "";
  const trail = parseTrail(params.get("trail") || "");
  const page = trail.length + 1;
  el.innerHTML = `<div class="page-head"><h1>Audit log</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: String(PAGE_SIZE) });
  if (appID) qs.set("app_id", appID);
  if (action) qs.set("action", action);
  if (cursor) qs.set("cursor", cursor);
  const data = await api("GET", "/audit-log?" + qs.toString());
  const items = data.items || [];
  const total = Number(data.total ?? items.length);
  const nextCursor = data.next_cursor || "";

  const rows = items.map((a, i) => {
    const diff = diffPreview(a.diff);
    const targetType = a.target_type || "";
    const targetID = a.target_id || "";
    return `<tr class="aud-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String((page - 1) * PAGE_SIZE + i + 1).padStart(2, "0")}</td>
      <td class="user-when">${esc(fmtTime(a.created_at))}</td>
      <td><span class="aud-action mono">${esc(a.action || "—")}</span></td>
      <td class="aud-app mono" title="${esc(a.app_id || "")}">${a.app_id ? esc(shortRef(a.app_id)) : `<span class="user-empty">—</span>`}</td>
      <td>
        <div class="aud-target">
          <span class="aud-target__type">${targetType ? esc(targetType) : "—"}</span>
          <span class="aud-target__id mono" title="${esc(targetID)}">${targetID ? esc(shortRef(targetID)) : ""}</span>
        </div>
      </td>
      <td class="aud-diff mono" title="${esc(diff)}">${diff ? esc(diff) : `<span class="user-empty">—</span>`}</td>
      <td class="aud-ip mono">${a.ip ? esc(a.ip) : `<span class="user-empty">—</span>`}</td>
      <td class="aud-req mono" title="${esc(a.request_id || "")}">${a.request_id ? esc(shortRef(a.request_id)) : `<span class="user-empty">—</span>`}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="8" class="empty user-empty-row">No audit entries.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">Platform · compliance</p>
        <h1 class="user-page-title">Audit log</h1>
        <p class="muted">Admin mutations across apps — who changed what, and when.</p>
      </div>
      <div class="user-count" aria-label="Audit entries">${total}</div>
    </div>

    <form id="filters" class="toolbar user-search ops-filters">
      <label class="user-search__label" for="aud-app">Filter</label>
      <div class="toolbar-fields ops-filters__fields">
        <input id="aud-app" name="app_id" value="${esc(appID)}" placeholder="App ID" class="mono" autocomplete="off">
        <input name="action" value="${esc(action)}" placeholder="Action e.g. app.create" autocomplete="off">
      </div>
      <button type="submit">Filter</button>
    </form>

    <div class="table-wrap apps-table-wrap aud-table-wrap">
      <table class="apps-table aud-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>When</th><th>Action</th><th>App</th><th>Target</th><th>Diff</th><th>IP</th><th>Request</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    <nav class="user-pager" aria-label="Audit pagination">
      <button type="button" class="user-pager__btn" id="aud-prev" ${trail.length ? "" : "disabled"}>Prev</button>
      <span class="user-pager__meta">Page ${page} · ${items.length} shown</span>
      <button type="button" class="user-pager__btn" id="aud-next" ${nextCursor ? "" : "disabled"}>Next</button>
    </nav>`;

  applyRevealDelays(el, ".aud-row[data-reveal]");

  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    location.hash = auditHash({
      appID: String(fd.get("app_id") || "").trim(),
      action: String(fd.get("action") || "").trim(),
    });
  });

  el.querySelector("#aud-prev").addEventListener("click", () => {
    if (!trail.length) return;
    const prev = trail[trail.length - 1] || "";
    location.hash = auditHash({ appID, action, cursor: prev, trail: trail.slice(0, -1) });
  });

  el.querySelector("#aud-next").addEventListener("click", () => {
    if (!nextCursor) return;
    location.hash = auditHash({ appID, action, cursor: nextCursor, trail: [...trail, cursor] });
  });
}
