import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

function referralStamp(status) {
  const s = status || "unknown";
  let tone = "unknown";
  if (s === "rewarded") tone = "active";
  else if (s === "pending") tone = "pending";
  else if (s === "void") tone = "disabled";
  return `<span class="user-stamp user-stamp--${tone}">${esc(s)}</span>`;
}

function partyCell(appID, party) {
  const id = party?.id || "";
  const email = party?.email || "";
  if (!id && !email) return `<span class="user-empty">—</span>`;
  if (id && email) {
    return `<a class="ref-party" href="#/apps/${esc(appID)}/users/${esc(id)}">
      <span class="ref-party__email">${esc(email)}</span>
      <span class="ref-party__id mono">${esc(id)}</span>
    </a>`;
  }
  if (id) {
    return `<a class="ref-party ref-party--id mono" href="#/apps/${esc(appID)}/users/${esc(id)}">${esc(id)}</a>`;
  }
  return `<span class="ref-party__email">${esc(email)}</span>`;
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

export async function renderReferrals(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const status = params.get("status") || "";
  el.innerHTML = `<div class="page-head"><h1>Referrals</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: "50" });
  if (status) qs.set("status", status);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/referrals?" + qs.toString());
  const items = data.items || [];
  const activeN = items.filter((r) => r.status !== "void").length;

  const rows = items.map((r, i) => {
    const referrer = r.referrer || {};
    const referee = r.referee || {};
    const voidBtn = r.status !== "void"
      ? `<button type="button" class="ref-void void-ref" data-id="${esc(r.id)}">Void</button>`
      : `<span class="user-empty">—</span>`;
    return `<tr class="ref-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>${partyCell(appID, referrer)}</td>
      <td>${partyCell(appID, referee)}</td>
      <td class="ref-code mono">${esc(r.code || "—")}</td>
      <td>${referralStamp(r.status)}</td>
      <td class="ref-rewards">
        <span class="ref-rewards__count">${esc(r.reward_count)}</span>
        <span class="ref-rewards__sep">/</span>
        <span class="ref-rewards__credits">${esc(r.rewarded_credits)} cr</span>
      </td>
      <td class="user-when">${esc(fmtTime(r.created_at))}</td>
      <td class="ref-actions">${voidBtn}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="8" class="empty user-empty-row">No referrals.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · growth</p>
        <h1 class="user-page-title">Referrals</h1>
        <p class="muted">Invite attribution and reward ledger for this app.</p>
      </div>
      <div class="user-count" aria-label="Active referrals">${activeN}<span class="ov-bound-total">/${items.length}</span></div>
    </div>

    <form id="ref-filters" class="toolbar user-search ref-filters">
      <label class="user-search__label" for="filter-status">Filter</label>
      <div class="toolbar-fields ref-filters__fields">
        <div class="field field--choice ref-filters__select">
          <select id="filter-status" name="status" aria-label="Status">
            <option value="">Any status</option>
            <option value="pending"${status === "pending" ? " selected" : ""}>pending</option>
            <option value="rewarded"${status === "rewarded" ? " selected" : ""}>rewarded</option>
            <option value="void"${status === "void" ? " selected" : ""}>void</option>
          </select>
        </div>
      </div>
    </form>

    <div class="table-wrap apps-table-wrap ref-table-wrap">
      <table class="apps-table ref-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>Referrer</th><th>Referee</th><th>Code</th><th>Status</th><th>Rewards</th><th>Created</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>`;

  applyRevealDelays(el, ".ref-row[data-reveal]");
  wireMobileSelect(el.querySelector("#filter-status"), { title: "Status" });

  el.querySelector("#filter-status").addEventListener("change", (ev) => {
    const v = ev.target.value;
    location.hash = "#/apps/" + appID + "/referrals" + (v ? "?status=" + encodeURIComponent(v) : "");
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
