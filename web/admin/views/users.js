import { api, esc, fmtTime, showOk } from "../api.js";

export async function renderUsers(el, appID) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const q = params.get("q") || "";
  el.innerHTML = `<h1>Users</h1><p class="muted">Loading…</p>`;
  const qs = new URLSearchParams({ limit: "50" });
  if (q) qs.set("q", q);
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/users?" + qs.toString());
  const items = data.items || [];
  const rows = items.map((u) => `
    <tr>
      <td><a href="#/apps/${esc(appID)}/users/${esc(u.id)}">${esc(u.email)}</a></td>
      <td class="mono">${esc(u.id)}</td>
      <td>${esc(u.status)}</td>
      <td>${esc(u.balance)}</td>
      <td>${esc(fmtTime(u.last_login_at))}</td>
      <td>${esc(fmtTime(u.created_at))}</td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty">No users.</td></tr>`;

  el.innerHTML = `
    <h1>Users</h1>
    <form id="search" class="toolbar">
      <div class="toolbar-fields">
        <input name="q" value="${esc(q)}" placeholder="Search email or ID">
      </div>
      <button type="submit">Search</button>
    </form>
    <div class="table-wrap">
    <table>
      <thead><tr><th>Email</th><th>ID</th><th>Status</th><th>Balance</th><th>Last login</th><th>Created</th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>`;

  el.querySelector("#search").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const qq = new FormData(ev.target).get("q");
    location.hash = "#/apps/" + appID + "/users" + (qq ? "?q=" + encodeURIComponent(String(qq)) : "");
    renderUsers(el, appID);
  });
}

export async function renderUserDetail(el, appID, userID) {
  el.innerHTML = `<h1>User</h1><p class="muted">Loading…</p>`;
  const base = "/apps/" + encodeURIComponent(appID) + "/users/" + encodeURIComponent(userID);
  const user = await api("GET", base);
  const ledger = await api("GET", base + "/ledger?limit=50");
  const ledgerRows = (ledger.items || []).map((e) => `
    <tr>
      <td>${esc(fmtTime(e.created_at))}</td>
      <td class="mono">${esc(e.entry_type)}</td>
      <td>${esc(e.amount)}</td>
      <td>${esc(e.balance_after)}</td>
      <td>${esc(e.actor)}</td>
      <td>${esc(e.reason || "")}</td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty">No ledger entries.</td></tr>`;

  const clearDebtBtn = user.balance < 0
    ? `<button type="button" class="danger" id="clear-debt">Clear debt</button>`
    : "";

  el.innerHTML = `
    <h1>${esc(user.email || user.id)}</h1>
    <p><a href="#/apps/${esc(appID)}/users">← Users</a></p>
    <article>
      <p><span class="muted">ID</span> <span class="mono">${esc(user.id)}</span></p>
      <p><span class="muted">Status</span> ${esc(user.status)}
        <button type="button" class="secondary" id="toggle-status">
          ${user.status === "disabled" ? "Enable" : "Disable"}
        </button>
      </p>
      <p><span class="muted">Balance</span> <strong>${esc(user.balance)}</strong> ${clearDebtBtn}</p>
      <p><span class="muted">Display name</span> ${esc(user.display_name || "—")}</p>
      <p><span class="muted">Created</span> ${esc(fmtTime(user.created_at))}</p>
    </article>
    <article>
      <h2>Adjust credits</h2>
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
          <button type="submit">Apply</button>
        </div>
      </form>
    </article>
    <h2>Ledger</h2>
    <p class="muted"><a href="#/apps/${esc(appID)}/activity?view=ledger&user_id=${esc(userID)}">App ledger</a>
      · <a href="#/apps/${esc(appID)}/activity?user_id=${esc(userID)}">App purchases</a></p>
    <div class="table-wrap">
    <table>
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
}
