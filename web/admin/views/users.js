import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";
import { getAppsCache } from "../context.js";

function appLabel(appID) {
  const app = (getAppsCache() || []).find((a) => a.id === appID);
  if (!app) return { name: "", slug: shortRef(appID) };
  return { name: app.name || "", slug: app.slug || shortRef(appID) };
}

function initials(email, name) {
  const src = (name || email || "?").trim();
  if (!src) return "?";
  if (src.includes("@")) return src.slice(0, 1).toUpperCase();
  const parts = src.split(/\s+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return src.slice(0, 2).toUpperCase();
}

function statusChip(status) {
  const s = status || "unknown";
  return `<span class="user-stamp user-stamp--${esc(s)}">${esc(s)}</span>`;
}

function avatarFallback(label, sizeClass = "") {
  return `<span class="user-avatar user-avatar--fallback ${sizeClass}" aria-hidden="true">${esc(label)}</span>`;
}

function avatarThumb(u, sizeClass = "") {
  const label = initials(u.email, u.display_name);
  if (u.avatar_url) {
    // no-referrer: many R2/CDN public buckets reject hotlinks that send a Referer.
    return `<img class="user-avatar ${sizeClass}" src="${esc(u.avatar_url)}" alt="" width="40" height="40" referrerpolicy="no-referrer" decoding="async" data-fallback="${esc(label)}">`;
  }
  return avatarFallback(label, sizeClass);
}

function wireAvatarFallbacks(root) {
  root.querySelectorAll("img.user-avatar[data-fallback]").forEach((img) => {
    img.addEventListener("error", () => {
      const span = document.createElement("span");
      span.className = img.className + " user-avatar--fallback";
      span.setAttribute("aria-hidden", "true");
      span.textContent = img.getAttribute("data-fallback") || "?";
      img.replaceWith(span);
    }, { once: true });
  });
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((el) => {
    const n = Number(el.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) el.style.setProperty("--reveal", String(n));
  });
}

function cellOrDash(value, mono = false) {
  if (!value) return `<span class="user-empty">—</span>`;
  return `<span class="${mono ? "mono " : ""}user-cell">${esc(value)}</span>`;
}

function shortRef(id) {
  if (!id) return "—";
  return id.length > 10 ? id.slice(0, 8) : id;
}

const USERS_FIRST = "_";

function encodeUsersCursor(c) {
  return c || USERS_FIRST;
}

function decodeUsersCursor(c) {
  return !c || c === USERS_FIRST ? "" : c;
}

function usersHash(appID, { q = "", cursor = "", trail = [] } = {}) {
  const p = new URLSearchParams();
  if (q) p.set("q", q);
  if (cursor) p.set("cursor", cursor);
  if (trail.length) p.set("trail", trail.map(encodeUsersCursor).join("|"));
  const qs = p.toString();
  return "#/apps/" + appID + "/users" + (qs ? "?" + qs : "");
}

function parseTrail(raw) {
  if (!raw) return [];
  return raw.split("|").filter((p) => p !== "").map(decodeUsersCursor);
}

export async function renderUsers(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const q = params.get("q") || "";
  const cursor = params.get("cursor") || "";
  const trail = parseTrail(params.get("trail") || "");
  const page = trail.length + 1;
  const pageSize = 50;
  const app = appLabel(appID);
  el.innerHTML = `<div class="page-head"><h1>Users</h1><p class="muted">Loading…</p></div>`;
  const qs = new URLSearchParams({ limit: String(pageSize) });
  if (q) qs.set("q", q);
  if (cursor) qs.set("cursor", cursor);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/users?" + qs.toString());
  const items = data.items || [];
  const total = Number(data.total ?? items.length);
  const nextCursor = data.next_cursor || "";
  const rows = items.map((u, i) => `
    <tr class="user-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String((page - 1) * pageSize + i + 1).padStart(2, "0")}</td>
      <td>
        <a class="user-identity" href="#/apps/${esc(appID)}/users/${esc(u.id)}">
          ${avatarThumb(u)}
          <span class="user-identity__text">
            <span class="user-identity__email">${esc(u.email || "—")}</span>
            <span class="user-identity__sub">${esc(u.display_name || "—")}</span>
          </span>
        </a>
      </td>
      <td class="user-id mono" title="${esc(u.id)}">${esc(u.id)}</td>
      <td>${statusChip(u.status)}</td>
      <td class="user-balance">${esc(u.balance)}</td>
      <td class="user-attr" title="First-touch utm_source">${cellOrDash(u.utm_source)}</td>
      <td class="user-locale">${cellOrDash(u.locale)}</td>
      <td class="user-ip">${cellOrDash(u.last_ip, true)}</td>
      <td class="user-when">${esc(fmtTime(u.created_at))}</td>
      <td class="muted user-when">${esc(fmtTime(u.last_login_at))}</td>
    </tr>`).join("") || `<tr><td colspan="10" class="empty user-empty-row">No users.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">Ops · accounts</p>
        <h1 class="user-page-title">
          Users
          <span class="user-app-mark" title="${esc(app.name || app.slug)}">${esc(app.slug)}</span>
        </h1>
        <p class="muted">Manage app accounts — status, balance, locale, attribution, and login activity.</p>
      </div>
      <div class="user-count" aria-label="Total users">${total}</div>
    </div>
    <form id="search" class="toolbar user-search">
      <label class="user-search__label" for="user-q">Find</label>
      <div class="toolbar-fields">
        <input id="user-q" name="q" value="${esc(q)}" placeholder="email or user id" autocomplete="off">
      </div>
      <button type="submit">Filter</button>
    </form>
    <div class="table-wrap user-table-wrap">
    <table class="user-table">
      <thead><tr>
        <th class="user-idx-h"></th>
        <th>Identity</th>
        <th>ID</th>
        <th>Status</th>
        <th>Balance</th>
        <th>Attribution</th>
        <th>Locale</th>
        <th>Last IP</th>
        <th>Created</th>
        <th>Last login</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>
    <nav class="user-pager" aria-label="Users pagination">
      <button type="button" class="user-pager__btn" id="user-prev" ${trail.length ? "" : "disabled"}>Prev</button>
      <span class="user-pager__meta">Page ${page} · ${items.length} shown</span>
      <button type="button" class="user-pager__btn" id="user-next" ${nextCursor ? "" : "disabled"}>Next</button>
    </nav>`;

  el.querySelector("#search").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const qq = new FormData(ev.target).get("q");
    // hashchange → route() re-renders; do not call renderUsers here (double fetch).
    location.hash = usersHash(appID, { q: String(qq || "") });
  });

  el.querySelector("#user-prev").addEventListener("click", () => {
    if (!trail.length) return;
    const prev = trail[trail.length - 1] || "";
    location.hash = usersHash(appID, { q, cursor: prev, trail: trail.slice(0, -1) });
  });

  el.querySelector("#user-next").addEventListener("click", () => {
    if (!nextCursor) return;
    location.hash = usersHash(appID, { q, cursor: nextCursor, trail: [...trail, cursor] });
  });

  wireAvatarFallbacks(el);
  applyRevealDelays(el, ".user-row[data-reveal]");
}

export async function renderUserDetail(el, appID, userID) {
  el.innerHTML = `<div class="page-head"><h1>User</h1><p class="muted">Loading…</p></div>`;
  const base = "/apps/" + encodeURIComponent(appID) + "/users/" + encodeURIComponent(userID);
  const user = await api("GET", base);
  const ledger = await api("GET", base + "/ledger?limit=50");
  const ledgerRows = (ledger.items || []).map((e, i) => `
    <tr class="user-ledger-row" data-reveal="${i}">
      <td class="user-when">${esc(fmtTime(e.created_at))}</td>
      <td class="user-ledger-type">${esc(e.entry_type)}</td>
      <td class="user-balance">${esc(e.amount)}</td>
      <td class="user-balance">${esc(e.balance_after)}</td>
      <td>${esc(e.actor)}</td>
      <td>${esc(e.reason || "")}</td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty user-empty-row">No ledger entries.</td></tr>`;

  const clearDebtBtn = user.balance < 0
    ? `<button type="button" class="danger" id="clear-debt">Clear debt</button>`
    : "";

  const title = user.display_name || user.email || user.id;
  const subtitle = user.email && user.display_name ? user.email : user.id;
  const balClass = user.balance < 0 ? " user-dossier__balance--debt" : "";

  el.innerHTML = `
    <p class="user-back"><a href="#/apps/${esc(appID)}/users">← Users</a></p>

    <header class="user-dossier">
      <div class="user-dossier__rail" aria-hidden="true">CASE</div>
      <div class="user-dossier__avatar">
        ${avatarThumb(user, "user-avatar--lg")}
      </div>
      <div class="user-dossier__body">
        <div class="user-dossier__meta-line">
          <span class="user-dossier__ref">REF ${esc(shortRef(user.id))}</span>
          ${statusChip(user.status)}
        </div>
        <h1 class="user-dossier__title">${esc(title)}</h1>
        <p class="user-dossier__sub">${esc(subtitle)}</p>
        <div class="user-dossier__actions">
          <button type="button" class="secondary" id="toggle-status">
            ${user.status === "disabled" ? "Enable account" : "Disable account"}
          </button>
          ${clearDebtBtn}
        </div>
      </div>
      <div class="user-dossier__balance${balClass}" aria-label="Credit balance">
        <span class="user-dossier__balance-label">Balance</span>
        <strong class="user-dossier__balance-value">${esc(user.balance)}</strong>
        <span class="user-dossier__balance-hint">credits</span>
      </div>
    </header>

    <section class="user-meta-grid" aria-label="Profile fields">
      <div class="user-meta user-meta--wide">
        <span class="user-meta__k">User ID</span>
        <span class="user-meta__v user-meta__v--id">${esc(user.id)}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Locale</span>
        <span class="user-meta__v">${esc(user.locale || "—")}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Last IP</span>
        <span class="user-meta__v">${esc(user.last_ip || "—")}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">UTM source</span>
        <span class="user-meta__v">${esc(user.utm_source || "—")}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Last login</span>
        <span class="user-meta__v">${esc(fmtTime(user.last_login_at))}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Created</span>
        <span class="user-meta__v">${esc(fmtTime(user.created_at))}</span>
      </div>
    </section>

    <article class="user-adjust">
      <div class="user-adjust__head">
        <h2>Adjust credits</h2>
        <span class="user-adjust__badge">manual</span>
      </div>
      <form id="adjust">
        <div class="form-row">
          <div class="field"><label>Entry type</label>
            <select name="entry_type">
              <option value="admin_adjust">admin_adjust</option>
              <option value="affiliate_payout">affiliate_payout</option>
            </select>
          </div>
          <div class="field"><label>Amount</label><input name="amount" type="number" required></div>
          <div class="field"><label>Reason</label><input name="reason" required maxlength="500"></div>
        </div>
        <div class="form-actions">
          <button type="submit">Post entry</button>
        </div>
      </form>
    </article>

    <div class="user-ledger-head">
      <h2>Ledger</h2>
      <div class="user-ledger-links" aria-label="Related activity">
        <a class="user-ledger-link" href="#/apps/${esc(appID)}/activity?view=ledger&user_id=${esc(userID)}">App ledger</a>
        <a class="user-ledger-link" href="#/apps/${esc(appID)}/activity?user_id=${esc(userID)}">App purchases</a>
      </div>
    </div>
    <div class="table-wrap user-ledger-wrap">
    <table class="user-ledger">
      <thead><tr><th>When</th><th>Type</th><th>Amount</th><th>Balance after</th><th>Actor</th><th>Reason</th></tr></thead>
      <tbody>${ledgerRows}</tbody>
    </table>
    </div>`;

  el.querySelector("#toggle-status").addEventListener("click", async () => {
    const status = user.status === "disabled" ? "active" : "disabled";
    if (status === "disabled" && !confirm("Disable this user? Refresh tokens will be revoked.")) return;
    await api("PATCH", base, { status });
    showOk("User " + status);
    await renderUserDetail(el, appID, userID);
  });

  const clearBtn = el.querySelector("#clear-debt");
  if (clearBtn) {
    clearBtn.addEventListener("click", async () => {
      if (!confirm("Clear negative balance to zero?")) return;
      const reason = prompt("Reason (optional)", "debt cleared") || "debt cleared";
      await api("POST", base + "/credits/clear-debt", { reason });
      showOk("Debt cleared");
      await renderUserDetail(el, appID, userID);
    });
  }

  wireMobileSelect(el.querySelector('#adjust select[name="entry_type"]'), { title: "Entry type" });

  el.querySelector("#adjust").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    await api("POST", base + "/credits", {
      entry_type: fd.get("entry_type"),
      amount: Number(fd.get("amount")),
      reason: fd.get("reason"),
    });
    showOk("Credits adjusted");
    await renderUserDetail(el, appID, userID);
  });

  wireAvatarFallbacks(el);
  applyRevealDelays(el, ".user-ledger-row[data-reveal]");
}
