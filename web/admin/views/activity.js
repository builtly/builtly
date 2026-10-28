import { api, esc, fmtTime } from "../api.js";

const purchaseStatuses = ["pending", "completed", "partially_refunded", "refunded", "failed", "expired"];
const providers = ["stripe", "apple", "google"];
const entryTypes = [
  "signup_bonus", "referral_bonus", "stripe_purchase", "iap_purchase", "ai_consume",
  "refund", "refund_reversal", "affiliate_payout", "affiliate_reversal", "admin_adjust",
];

function hashFor(appID, view, filters) {
  const qs = new URLSearchParams();
  if (view === "ledger") qs.set("view", "ledger");
  for (const [k, v] of Object.entries(filters)) {
    if (v) qs.set(k, v);
  }
  const q = qs.toString();
  return "#/apps/" + appID + "/activity" + (q ? "?" + q : "");
}

function opt(value, current, label) {
  return `<option value="${esc(value)}"${value === current ? " selected" : ""}>${esc(label || value)}</option>`;
}

export async function renderActivity(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const view = params.get("view") === "ledger" ? "ledger" : "purchases";
  const q = params.get("q") || "";
  const userID = params.get("user_id") || "";
  el.innerHTML = `<h1>Activity</h1><p class="muted">Loading…</p>`;
  if (view === "ledger") {
    await renderLedger(el, appID, { q, userID, entryType: params.get("entry_type") || "" });
    return;
  }
  await renderPurchases(el, appID, {
    q, userID,
    status: params.get("status") || "",
    provider: params.get("provider") || "",
  });
}

function tabs(appID, view, filters) {
  const purchases = hashFor(appID, "purchases", { q: filters.q, user_id: filters.userID });
  const ledger = hashFor(appID, "ledger", { q: filters.q, user_id: filters.userID });
  return `<p class="toolbar">
    <a href="${esc(purchases)}"${view === "purchases" ? ' aria-current="page"' : ""}>Purchases</a>
    <a href="${esc(ledger)}"${view === "ledger" ? ' aria-current="page"' : ""}>Ledger</a>
  </p>`;
}

async function renderPurchases(el, appID, filters) {
  const qs = new URLSearchParams({ limit: "50" });
  if (filters.q) qs.set("q", filters.q);
  if (filters.userID) qs.set("user_id", filters.userID);
  if (filters.status) qs.set("status", filters.status);
  if (filters.provider) qs.set("provider", filters.provider);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/purchases?" + qs.toString());
  const rows = (data.items || []).map((p) => {
    const user = p.user_email
      ? `<a href="#/apps/${esc(appID)}/users/${esc(p.user_id)}">${esc(p.user_email)}</a>`
      : `<a class="mono" href="#/apps/${esc(appID)}/users/${esc(p.user_id)}">${esc(p.user_id)}</a>`;
    const money = p.amount == null ? "" : `${esc(p.amount)} ${esc(p.currency || "")}`;
    return `<tr>
      <td>${esc(fmtTime(p.created_at))}</td>
      <td>${user}</td>
      <td>${esc(p.provider)}</td>
      <td>${esc(p.status)}</td>
      <td>${esc(p.product_sku || p.product_id)}</td>
      <td>${esc(p.credits)}</td>
      <td>${esc(p.refunded_credits)}</td>
      <td>${money}</td>
      <td class="mono">${esc(p.payment_ref || p.provider_ref || "")}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="9" class="empty">No purchases.</td></tr>`;

  el.innerHTML = `
    <h1>Activity</h1>
    <p class="muted">App-wide credit-pack purchases and credit ledger. A single user's history is also on their user page.</p>
    ${tabs(appID, "purchases", filters)}
    <form id="filters" class="toolbar">
      <div class="toolbar-fields">
        <input name="q" value="${esc(filters.q)}" placeholder="Email or user ID prefix">
        <input name="user_id" value="${esc(filters.userID)}" placeholder="User ID">
        <label>Status
          <select name="status">
            ${opt("", filters.status, "all")}
            ${purchaseStatuses.map((s) => opt(s, filters.status)).join("")}
          </select>
        </label>
        <label>Provider
          <select name="provider">
            ${opt("", filters.provider, "all")}
            ${providers.map((s) => opt(s, filters.provider)).join("")}
          </select>
        </label>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap">
    <table>
      <thead><tr><th>When</th><th>User</th><th>Provider</th><th>Status</th><th>Product</th><th>Credits</th><th>Refunded</th><th>Amount</th><th>Ref</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;
  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    location.hash = hashFor(appID, "purchases", {
      q: String(fd.get("q") || "").trim(),
      user_id: String(fd.get("user_id") || "").trim(),
      status: String(fd.get("status") || ""),
      provider: String(fd.get("provider") || ""),
    });
  });
}

async function renderLedger(el, appID, filters) {
  const qs = new URLSearchParams({ limit: "50" });
  if (filters.q) qs.set("q", filters.q);
  if (filters.userID) qs.set("user_id", filters.userID);
  if (filters.entryType) qs.set("entry_type", filters.entryType);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/ledger?" + qs.toString());
  const rows = (data.items || []).map((e) => {
    const user = e.user_email
      ? `<a href="#/apps/${esc(appID)}/users/${esc(e.user_id)}">${esc(e.user_email)}</a>`
      : `<a class="mono" href="#/apps/${esc(appID)}/users/${esc(e.user_id)}">${esc(e.user_id)}</a>`;
    const ref = [e.reference_type, e.reference_id].filter(Boolean).join(" ");
    return `<tr>
      <td>${esc(fmtTime(e.created_at))}</td>
      <td>${user}</td>
      <td class="mono">${esc(e.entry_type)}</td>
      <td>${esc(e.amount)}</td>
      <td>${esc(e.balance_after)}</td>
      <td>${esc(e.actor)}</td>
      <td>${esc(e.reason || "")}</td>
      <td class="mono">${esc(ref)}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="8" class="empty">No ledger entries.</td></tr>`;

  el.innerHTML = `
    <h1>Activity</h1>
    <p class="muted">Credit movements for this app. <span class="mono">ai_consume</span> is metered usage; <span class="mono">stripe_purchase</span> and <span class="mono">iap_purchase</span> are top-ups.</p>
    ${tabs(appID, "ledger", filters)}
    <form id="filters" class="toolbar">
      <div class="toolbar-fields">
        <input name="q" value="${esc(filters.q)}" placeholder="Email or user ID prefix">
        <input name="user_id" value="${esc(filters.userID)}" placeholder="User ID">
        <label>Type
          <select name="entry_type">
            ${opt("", filters.entryType, "all")}
            ${entryTypes.map((s) => opt(s, filters.entryType)).join("")}
          </select>
        </label>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap">
    <table>
      <thead><tr><th>When</th><th>User</th><th>Type</th><th>Amount</th><th>Balance after</th><th>Actor</th><th>Reason</th><th>Reference</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;
  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    location.hash = hashFor(appID, "ledger", {
      q: String(fd.get("q") || "").trim(),
      user_id: String(fd.get("user_id") || "").trim(),
      entry_type: String(fd.get("entry_type") || ""),
    });
  });
}
