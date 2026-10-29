import { api, esc, fmtTime } from "../api.js";

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

export async function renderAudit(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const appID = params.get("app_id") || "";
  const action = params.get("action") || "";
  el.innerHTML = `<div class="page-head"><h1>Audit log</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: "100" });
  if (appID) qs.set("app_id", appID);
  if (action) qs.set("action", action);
  const data = await api("GET", "/audit-log?" + qs.toString());
  const items = data.items || [];

  const rows = items.map((a, i) => {
    const diff = diffPreview(a.diff);
    const targetType = a.target_type || "";
    const targetID = a.target_id || "";
    return `<tr class="aud-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
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
      <div class="user-count" aria-label="Audit entries">${items.length}</div>
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
    </div>`;

  applyRevealDelays(el, ".aud-row[data-reveal]");

  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    const q = new URLSearchParams();
    const a = String(fd.get("app_id") || "").trim();
    const act = String(fd.get("action") || "").trim();
    if (a) q.set("app_id", a);
    if (act) q.set("action", act);
    const qs2 = q.toString();
    location.hash = "#/audit" + (qs2 ? "?" + qs2 : "");
  });
}
