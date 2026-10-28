import { api, esc, fmtTime } from "../api.js";

export async function renderAudit(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const appID = params.get("app_id") || "";
  const action = params.get("action") || "";
  el.innerHTML = `<h1>Audit log</h1><p class="muted">Loading…</p>`;
  const qs = new URLSearchParams({ limit: "100" });
  if (appID) qs.set("app_id", appID);
  if (action) qs.set("action", action);
  const data = await api("GET", "/audit-log?" + qs.toString());
  const items = data.items || [];
  const rows = items.map((a) => `
    <tr>
      <td>${esc(fmtTime(a.created_at))}</td>
      <td class="mono">${esc(a.action)}</td>
      <td class="mono">${esc(a.app_id || "")}</td>
      <td>${esc(a.target_type)} <span class="mono">${esc(a.target_id)}</span></td>
      <td class="mono">${esc(typeof a.diff === "string" ? a.diff : JSON.stringify(a.diff))}</td>
      <td>${esc(a.ip)}</td>
      <td class="mono">${esc(a.request_id)}</td>
    </tr>`).join("") || `<tr><td colspan="7" class="empty">No audit entries.</td></tr>`;

  el.innerHTML = `
    <h1>Audit log</h1>
    <form id="filters" class="toolbar">
      <div class="toolbar-fields">
        <label>App ID
          <input name="app_id" value="${esc(appID)}" placeholder="uuid" class="mono">
        </label>
        <label>Action
          <input name="action" value="${esc(action)}" placeholder="e.g. app.create">
        </label>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap">
    <table>
      <thead><tr><th>When</th><th>Action</th><th>App</th><th>Target</th><th>Diff</th><th>IP</th><th>Request</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;

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
    renderAudit(el);
  });
}
