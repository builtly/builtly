import { api, esc, fmtTime, showOk } from "../api.js";

export async function renderOutbox(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const status = params.get("status") || "";
  const subscriber = params.get("subscriber") || "";
  el.innerHTML = `<h1>Outbox</h1><p class="muted">Loading…</p>`;
  const qs = new URLSearchParams({ limit: "100" });
  if (status) qs.set("status", status);
  if (subscriber) qs.set("subscriber", subscriber);
  const data = await api("GET", "/outbox?" + qs.toString());
  const items = data.items || [];
  const rows = items.map((r) => {
    const retry = r.status === "dead"
      ? `<button type="button" class="secondary retry" data-id="${esc(r.id)}">Retry</button>`
      : "";
    return `<tr>
      <td class="mono">${esc(r.id)}</td>
      <td class="mono">${esc(r.event_type)}</td>
      <td>${esc(r.subscriber)}</td>
      <td>${esc(r.status)}</td>
      <td>${esc(r.attempts)}</td>
      <td>${esc(r.last_error || "—")}</td>
      <td class="mono">${esc(JSON.stringify(r.payload || {}))}</td>
      <td>${esc(fmtTime(r.created_at))}</td>
      <td>${retry}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="9" class="empty">No outbox rows.</td></tr>`;

  el.innerHTML = `
    <h1>Outbox</h1>
    <form id="filters" class="toolbar">
      <div class="toolbar-fields">
        <label>Status
          <select name="status">
            <option value="">all</option>
            <option value="pending" ${status === "pending" ? "selected" : ""}>pending</option>
            <option value="processing" ${status === "processing" ? "selected" : ""}>processing</option>
            <option value="done" ${status === "done" ? "selected" : ""}>done</option>
            <option value="dead" ${status === "dead" ? "selected" : ""}>dead</option>
          </select>
        </label>
        <label>Subscriber
          <input name="subscriber" value="${esc(subscriber)}" placeholder="optional">
        </label>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap">
    <table>
      <thead><tr><th>ID</th><th>Event</th><th>Subscriber</th><th>Status</th><th>Attempts</th><th>Error</th><th>Payload</th><th>Created</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;

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
    renderOutbox(el);
  });

  el.querySelectorAll(".retry").forEach((btn) => {
    btn.addEventListener("click", async () => {
      await api("POST", "/outbox/" + encodeURIComponent(btn.dataset.id) + "/retry");
      showOk("Queued for retry");
      await renderOutbox(el);
    });
  });
}
