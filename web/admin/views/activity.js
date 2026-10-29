import { api, esc, fmtTime } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

const purchaseStatuses = ["pending", "completed", "partially_refunded", "refunded", "failed", "expired"];
const providers = ["stripe", "apple", "google"];
const entryTypes = [
  "signup_bonus", "referral_bonus", "stripe_purchase", "iap_purchase", "ai_consume",
  "refund", "refund_reversal", "affiliate_payout", "affiliate_reversal", "admin_adjust",
];

const ZERO_DECIMAL = new Set([
  "bif", "clp", "djf", "gnf", "jpy", "kmf", "krw", "mga", "pyg", "rwf", "ugx", "vnd", "vuv", "xaf", "xof", "xpf",
]);

const PAGE_SIZE = 50;
const CURSOR_FIRST = "_";

function currencyFactor(currency) {
  return ZERO_DECIMAL.has(String(currency || "").toLowerCase()) ? 1 : 100;
}

function moneyLabel(minor, currency) {
  if (minor == null || minor === "") return "";
  const n = Number(minor);
  if (!Number.isFinite(n)) return String(minor);
  const cur = String(currency || "").toLowerCase();
  const f = currencyFactor(cur);
  const major = f === 1 ? String(Math.round(n)) : (n / f).toFixed(2);
  return `${major} ${(cur || "").toUpperCase()}`.trim();
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

function hashFor(appID, view, filters) {
  const qs = new URLSearchParams();
  if (view === "ledger") qs.set("view", "ledger");
  for (const [k, v] of Object.entries(filters)) {
    if (v == null || v === "") continue;
    if (k === "trail" && Array.isArray(v)) {
      if (v.length) qs.set("trail", v.map(encodeCursor).join("|"));
      continue;
    }
    qs.set(k, v);
  }
  const q = qs.toString();
  return "#/apps/" + appID + "/activity" + (q ? "?" + q : "");
}

function opt(value, current, label) {
  return `<option value="${esc(value)}"${value === current ? " selected" : ""}>${esc(label || value)}</option>`;
}

function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

function purchaseStamp(status) {
  const s = status || "unknown";
  let tone = "unknown";
  if (s === "completed") tone = "active";
  else if (s === "pending") tone = "pending";
  else if (s === "partially_refunded" || s === "refunded" || s === "failed" || s === "expired") tone = "disabled";
  return `<span class="user-stamp user-stamp--${tone}">${esc(s.replace(/_/g, " "))}</span>`;
}

function providerMark(provider) {
  const p = provider || "—";
  return `<span class="act-provider">${esc(p)}</span>`;
}

function userCell(appID, id, email) {
  if (!id) return `<span class="user-empty">—</span>`;
  if (email) {
    return `<a class="act-user" href="#/apps/${esc(appID)}/users/${esc(id)}">
      <span class="act-user__email">${esc(email)}</span>
      <span class="act-user__id mono">${esc(id)}</span>
    </a>`;
  }
  return `<a class="act-user act-user--id mono" href="#/apps/${esc(appID)}/users/${esc(id)}">${esc(id)}</a>`;
}

function pagerHTML(page, shown, nextCursor, trailLen, idPrefix) {
  return `<nav class="user-pager" aria-label="Pagination">
    <button type="button" class="user-pager__btn" id="${idPrefix}-prev" ${trailLen ? "" : "disabled"}>Prev</button>
    <span class="user-pager__meta">Page ${page} · ${shown} shown</span>
    <button type="button" class="user-pager__btn" id="${idPrefix}-next" ${nextCursor ? "" : "disabled"}>Next</button>
  </nav>`;
}

export async function renderActivity(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const view = params.get("view") === "ledger" ? "ledger" : "purchases";
  const q = params.get("q") || "";
  const userID = params.get("user_id") || "";
  const cursor = params.get("cursor") || "";
  const trail = parseTrail(params.get("trail") || "");
  el.innerHTML = `<div class="page-head"><h1>Activity</h1><p class="muted">Loading…</p></div>`;
  if (view === "ledger") {
    await renderLedger(el, appID, {
      q, userID, entryType: params.get("entry_type") || "", cursor, trail,
    });
    return;
  }
  await renderPurchases(el, appID, {
    q, userID,
    status: params.get("status") || "",
    provider: params.get("provider") || "",
    cursor, trail,
  });
}

function tabs(appID, view, filters) {
  const base = { q: filters.q, user_id: filters.userID };
  const purchases = hashFor(appID, "purchases", base);
  const ledger = hashFor(appID, "ledger", base);
  return `<nav class="act-tabs" aria-label="Activity views">
    <a href="${esc(purchases)}"${view === "purchases" ? ' aria-current="page"' : ""}>Purchases</a>
    <a href="${esc(ledger)}"${view === "ledger" ? ' aria-current="page"' : ""}>Ledger</a>
  </nav>`;
}

function pageHead(subtitle, count, countLabel) {
  return `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · billing</p>
        <h1 class="user-page-title">Activity</h1>
        <p class="muted">${subtitle}</p>
      </div>
      <div class="user-count" aria-label="${esc(countLabel)}">${count}</div>
    </div>`;
}

async function renderPurchases(el, appID, filters) {
  const page = filters.trail.length + 1;
  const qs = new URLSearchParams({ limit: String(PAGE_SIZE) });
  if (filters.q) qs.set("q", filters.q);
  if (filters.userID) qs.set("user_id", filters.userID);
  if (filters.status) qs.set("status", filters.status);
  if (filters.provider) qs.set("provider", filters.provider);
  if (filters.cursor) qs.set("cursor", filters.cursor);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/purchases?" + qs.toString());
  const items = data.items || [];
  const total = Number(data.total ?? items.length);
  const nextCursor = data.next_cursor || "";
  const rows = items.map((p, i) => {
    const money = moneyLabel(p.amount, p.currency);
    const ref = p.payment_ref || p.provider_ref || "";
    return `<tr class="act-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String((page - 1) * PAGE_SIZE + i + 1).padStart(2, "0")}</td>
      <td class="user-when">${esc(fmtTime(p.created_at))}</td>
      <td>${userCell(appID, p.user_id, p.user_email)}</td>
      <td>${providerMark(p.provider)}</td>
      <td>${purchaseStamp(p.status)}</td>
      <td>
        <div class="act-product">
          <span class="act-product__sku mono">${esc(p.product_sku || p.product_id || "—")}</span>
        </div>
      </td>
      <td class="act-num">${esc(p.credits)}</td>
      <td class="act-num">${esc(p.refunded_credits)}</td>
      <td class="act-money">${money ? esc(money) : `<span class="user-empty">—</span>`}</td>
      <td class="act-ref mono" title="${esc(ref)}">${ref ? esc(ref) : `<span class="user-empty">—</span>`}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="10" class="empty user-empty-row">No purchases.</td></tr>`;

  const filterState = {
    q: filters.q, user_id: filters.userID, status: filters.status, provider: filters.provider,
  };

  el.innerHTML = `
    ${pageHead("Credit-pack purchases across Stripe and mobile IAP.", total, "Purchases total")}
    ${tabs(appID, "purchases", filters)}
    <form id="filters" class="toolbar user-search act-filters">
      <label class="user-search__label" for="act-q">Filter</label>
      <div class="toolbar-fields act-filters__fields">
        <input id="act-q" name="q" value="${esc(filters.q)}" placeholder="Email or user ID prefix" autocomplete="off">
        <input name="user_id" value="${esc(filters.userID)}" placeholder="User ID" autocomplete="off">
        <div class="field field--choice act-filters__select">
          <select name="status" id="act-status" aria-label="Status">
            ${opt("", filters.status, "Any status")}
            ${purchaseStatuses.map((s) => opt(s, filters.status)).join("")}
          </select>
        </div>
        <div class="field field--choice act-filters__select">
          <select name="provider" id="act-provider" aria-label="Provider">
            ${opt("", filters.provider, "Any provider")}
            ${providers.map((s) => opt(s, filters.provider)).join("")}
          </select>
        </div>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap apps-table-wrap act-table-wrap">
      <table class="apps-table act-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>When</th><th>User</th><th>Provider</th><th>Status</th><th>Product</th>
          <th>Credits</th><th>Refunded</th><th>Amount</th><th>Ref</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    ${pagerHTML(page, items.length, nextCursor, filters.trail.length, "act")}`;

  wireMobileSelect(el.querySelector("#act-status"), { title: "Status" });
  wireMobileSelect(el.querySelector("#act-provider"), { title: "Provider" });
  applyRevealDelays(el, ".act-row[data-reveal]");

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

  el.querySelector("#act-prev").addEventListener("click", () => {
    if (!filters.trail.length) return;
    const prev = filters.trail[filters.trail.length - 1] || "";
    location.hash = hashFor(appID, "purchases", {
      ...filterState, cursor: prev, trail: filters.trail.slice(0, -1),
    });
  });

  el.querySelector("#act-next").addEventListener("click", () => {
    if (!nextCursor) return;
    location.hash = hashFor(appID, "purchases", {
      ...filterState, cursor: nextCursor, trail: [...filters.trail, filters.cursor],
    });
  });
}

async function renderLedger(el, appID, filters) {
  const page = filters.trail.length + 1;
  const qs = new URLSearchParams({ limit: String(PAGE_SIZE) });
  if (filters.q) qs.set("q", filters.q);
  if (filters.userID) qs.set("user_id", filters.userID);
  if (filters.entryType) qs.set("entry_type", filters.entryType);
  if (filters.cursor) qs.set("cursor", filters.cursor);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/ledger?" + qs.toString());
  const items = data.items || [];
  const total = Number(data.total ?? items.length);
  const nextCursor = data.next_cursor || "";
  const rows = items.map((e, i) => {
    const ref = [e.reference_type, e.reference_id].filter(Boolean).join(" ");
    const amount = Number(e.amount);
    const amountClass = Number.isFinite(amount) && amount < 0 ? "act-num act-num--neg" : "act-num act-num--pos";
    return `<tr class="act-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String((page - 1) * PAGE_SIZE + i + 1).padStart(2, "0")}</td>
      <td class="user-when">${esc(fmtTime(e.created_at))}</td>
      <td>${userCell(appID, e.user_id, e.user_email)}</td>
      <td><span class="act-type mono">${esc(e.entry_type)}</span></td>
      <td class="${amountClass}">${esc(e.amount)}</td>
      <td class="act-num">${esc(e.balance_after)}</td>
      <td class="act-actor">${esc(e.actor || "—")}</td>
      <td class="act-reason" title="${esc(e.reason || "")}">${e.reason ? esc(e.reason) : `<span class="user-empty">—</span>`}</td>
      <td class="act-ref mono" title="${esc(ref)}">${ref ? esc(ref) : `<span class="user-empty">—</span>`}</td>
    </tr>`;
  }).join("") || `<tr><td colspan="9" class="empty user-empty-row">No ledger entries.</td></tr>`;

  const filterState = {
    q: filters.q, user_id: filters.userID, entry_type: filters.entryType,
  };

  el.innerHTML = `
    ${pageHead("Credit movements — purchases, usage, refunds, and admin adjusts.", total, "Ledger entries total")}
    ${tabs(appID, "ledger", filters)}
    <form id="filters" class="toolbar user-search act-filters">
      <label class="user-search__label" for="act-ledger-q">Filter</label>
      <div class="toolbar-fields act-filters__fields">
        <input id="act-ledger-q" name="q" value="${esc(filters.q)}" placeholder="Email or user ID prefix" autocomplete="off">
        <input name="user_id" value="${esc(filters.userID)}" placeholder="User ID" autocomplete="off">
        <div class="field field--choice act-filters__select">
          <select name="entry_type" id="act-entry-type" aria-label="Entry type">
            ${opt("", filters.entryType, "Any type")}
            ${entryTypes.map((s) => opt(s, filters.entryType)).join("")}
          </select>
        </div>
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap apps-table-wrap act-table-wrap">
      <table class="apps-table act-table act-table--ledger">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>When</th><th>User</th><th>Type</th><th>Amount</th><th>Balance</th>
          <th>Actor</th><th>Reason</th><th>Reference</th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>
    ${pagerHTML(page, items.length, nextCursor, filters.trail.length, "act")}`;

  wireMobileSelect(el.querySelector("#act-entry-type"), { title: "Type" });
  applyRevealDelays(el, ".act-row[data-reveal]");

  el.querySelector("#filters").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    location.hash = hashFor(appID, "ledger", {
      q: String(fd.get("q") || "").trim(),
      user_id: String(fd.get("user_id") || "").trim(),
      entry_type: String(fd.get("entry_type") || ""),
    });
  });

  el.querySelector("#act-prev").addEventListener("click", () => {
    if (!filters.trail.length) return;
    const prev = filters.trail[filters.trail.length - 1] || "";
    location.hash = hashFor(appID, "ledger", {
      ...filterState, cursor: prev, trail: filters.trail.slice(0, -1),
    });
  });

  el.querySelector("#act-next").addEventListener("click", () => {
    if (!nextCursor) return;
    location.hash = hashFor(appID, "ledger", {
      ...filterState, cursor: nextCursor, trail: [...filters.trail, filters.cursor],
    });
  });
}
