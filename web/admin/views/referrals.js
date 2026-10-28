import { api, esc, fmtTime, showOk } from "../api.js";

export async function renderReferrals(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const status = params.get("status") || "";
  el.innerHTML = `<h1>Referrals</h1><p class="muted">Loading…</p>`;
  const qs = new URLSearchParams({ limit: "50" });
  if (status) qs.set("status", status);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/referrals?" + qs.toString());
  const items = data.items || [];
  const rows = items.map((r) => {
    const referrer = r.referrer || {};
    const referee = r.referee || {};
    const voidBtn = r.status !== "void"
      ? `<button type="button" class="danger void-ref" data-id="${esc(r.id)}">Void</button>`
      : "";
    return `<tr>
      <td class="mono">${esc(r.id)}</td>
      <td>${esc(referrer.email || referrer.id || "")}</td>
      <td>${esc(referee.email || referee.id || "")}</td>
      <td class="mono">${esc(r.code)}</td>
      <td>${esc(r.status)}</td>
      <td>${esc(r.reward_count)} / ${esc(r.rewarded_credits)}</td>
      <td>${esc(fmtTime(r.created_at))}</td>
      <td>${voidBtn}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="8" class="empty">No referrals.</td></tr>`;

  el.innerHTML = `
    <h1>Referrals</h1>
    <div class="toolbar">
      <div class="toolbar-fields">
        <label>Status
          <select id="filter-status">
            <option value="">all</option>
            <option value="pending" ${status === "pending" ? "selected" : ""}>pending</option>
            <option value="rewarded" ${status === "rewarded" ? "selected" : ""}>rewarded</option>
            <option value="void" ${status === "void" ? "selected" : ""}>void</option>
          </select>
        </label>
      </div>
    </div>
    <div class="table-wrap">
    <table>
      <thead><tr><th>ID</th><th>Referrer</th><th>Referee</th><th>Code</th><th>Status</th><th>Rewards</th><th>Created</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;

  el.querySelector("#filter-status").addEventListener("change", (ev) => {
    const v = ev.target.value;
    location.hash = "#/apps/" + appID + "/referrals" + (v ? "?status=" + encodeURIComponent(v) : "");
    renderReferrals(el, appID);
  });

  el.querySelectorAll(".void-ref").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const reason = prompt("Reason for voiding");
      if (reason == null) return;
      await api("POST",
        "/apps/" + encodeURIComponent(appID) + "/referrals/" + encodeURIComponent(btn.dataset.id) + "/void",
        { reason });
      showOk("Referral voided");
      await renderReferrals(el, appID);
    });
  });
}
