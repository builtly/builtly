import { api, esc, fmtTime, showOk } from "../api.js";
import { clearAppsCache, setCurrentAppID } from "../context.js";

const PROVIDER_GROUPS = [
  { id: "auth", label: "Auth", providers: ["google_oauth", "github_oauth", "apple_signin"] },
  { id: "payments", label: "Payments", providers: ["stripe", "apple_iap", "google_play"] },
  { id: "email", label: "Email", providers: ["resend"] },
  { id: "storage", label: "Storage", providers: ["r2"] },
  { id: "analytics", label: "Analytics", providers: ["posthog"] },
  { id: "affiliates", label: "Affiliates", providers: ["promotekit", "affonso"] },
];

const PROVIDERS = PROVIDER_GROUPS.flatMap((g) => g.providers);

const PROVIDER_LABELS = {
  stripe: "Stripe",
  resend: "Resend",
  r2: "Cloudflare R2",
  google_oauth: "Google OAuth",
  github_oauth: "GitHub OAuth",
  apple_signin: "Apple Sign In",
  apple_iap: "Apple IAP",
  google_play: "Google Play",
  posthog: "PostHog",
  promotekit: "PromoteKit",
  affonso: "Affonso",
};

function appStatusStamp(status) {
  const s = status || "unknown";
  return `<span class="user-stamp user-stamp--${esc(s === "suspended" ? "disabled" : s)}">${esc(s)}</span>`;
}

export async function renderAppsList(el) {
  el.innerHTML = `<div class="page-head"><h1>Apps</h1><p class="muted">Loading…</p></div>`;
  const data = await api("GET", "/apps?limit=100");
  const items = data.items || [];
  const rows = items.map((a, i) => `
    <tr class="apps-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <a class="apps-identity" href="#/apps/${esc(a.id)}/config">
          <span class="apps-identity__name">${esc(a.name)}</span>
          <span class="apps-identity__slug">${esc(a.slug)}</span>
        </a>
      </td>
      <td>${appStatusStamp(a.status)}</td>
      <td class="apps-id" title="${esc(a.id)}">${esc(a.id)}</td>
      <td class="user-when">${esc(fmtTime(a.created_at))}</td>
      <td class="apps-actions">
        <a class="apps-open" href="#/apps/${esc(a.id)}/config">Open</a>
      </td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty user-empty-row">No apps yet — create one to get started.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">Platform · catalog</p>
        <h1 class="user-page-title">Apps</h1>
        <p class="muted">Create an app first, then bind integrations and issue API keys under it.</p>
      </div>
      <div class="user-count" aria-label="Total apps">${items.length}</div>
    </div>

    <article class="apps-create">
      <div class="apps-create__head">
        <h2>Create app</h2>
        <span class="apps-create__badge">new</span>
      </div>
      <form id="create-app">
        <div class="form-row">
          <div class="field"><label>Slug</label><input name="slug" required pattern="[a-z0-9-]{3,40}" placeholder="my-app" autocomplete="off"></div>
          <div class="field"><label>Name</label><input name="name" required placeholder="My App" autocomplete="off"></div>
        </div>
        <div class="form-actions">
          <button type="submit">Create &amp; configure</button>
        </div>
      </form>
    </article>

    <div class="table-wrap apps-table-wrap">
      <table class="apps-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>App</th><th>Status</th><th>ID</th><th>Created</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>`;

  el.querySelectorAll(".apps-row[data-reveal]").forEach((row) => {
    const n = Number(row.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) row.style.setProperty("--reveal", String(n));
  });

  el.querySelector("#create-app").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    const app = await api("POST", "/apps", {
      slug: fd.get("slug"),
      name: fd.get("name"),
      settings: {},
    });
    clearAppsCache();
    setCurrentAppID(app.id);
    showOk("Created " + app.slug);
    location.hash = "#/apps/" + app.id + "/config";
  });
}

export async function renderAppDetail(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>Overview</h1><p class="muted">Loading…</p></div>`;
  const app = await api("GET", "/apps/" + encodeURIComponent(appID));
  const cfg = await api("GET", "/apps/" + encodeURIComponent(appID) + "/config");
  const integ = cfg.integrations || {};
  const bound = PROVIDERS.filter((p) => integ[p] && integ[p].connection_id).length;

  el.innerHTML = `
    <div class="page-head">
      <h1>Overview</h1>
      <p class="muted">App identity and integration status.</p>
    </div>
    <div class="panel">
      <form id="patch-app">
        <div class="form-row">
          <div class="field"><label>Name</label><input name="name" value="${esc(app.name)}" required></div>
          <div class="field"><label>Status</label>
            <select name="status">
              <option value="active" ${app.status === "active" ? "selected" : ""}>active</option>
              <option value="suspended" ${app.status === "suspended" ? "selected" : ""}>suspended</option>
            </select>
          </div>
        </div>
        <p class="muted mono">id ${esc(app.id)} · slug ${esc(app.slug)}</p>
        <div class="form-actions">
          <button type="submit">Save</button>
        </div>
      </form>
    </div>
    <div class="stat-row">
      <div class="stat">
        <div class="stat-label">Integrations bound</div>
        <div class="stat-value">${bound} / ${PROVIDERS.length}</div>
      </div>
      <div class="stat">
        <div class="stat-label">Next step</div>
        <div class="stat-value"><a href="#/apps/${esc(appID)}/config">Configure integrations →</a></div>
      </div>
    </div>
    <div class="table-wrap">
      <table>
        <thead><tr><th>Provider</th><th>Source</th><th>Connection</th></tr></thead>
        <tbody>
          ${PROVIDERS.map((p) => {
            const e = integ[p] || {};
            return `<tr>
              <td>${esc(PROVIDER_LABELS[p] || p)}</td>
              <td>${esc(e.source || "—")}</td>
              <td class="mono muted">${esc(e.connection_id || "not bound")}</td>
            </tr>`;
          }).join("")}
        </tbody>
      </table>
    </div>`;
  el.querySelector("#patch-app").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    await api("PATCH", "/apps/" + encodeURIComponent(appID), {
      name: fd.get("name"),
      status: fd.get("status"),
    });
    clearAppsCache();
    showOk("App updated");
    await renderAppDetail(el, appID);
  });
}

export async function renderAppConfig(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>Integrations</h1><p class="muted">Loading…</p></div>`;
  const cfg = await api("GET", "/apps/" + encodeURIComponent(appID) + "/config");
  const connections = await api("GET", "/connections");
  const byProvider = {};
  for (const c of connections.items || []) {
    (byProvider[c.provider] || (byProvider[c.provider] = [])).push(c);
  }
  const settingsJSON = JSON.stringify(cfg.settings || {}, null, 2);

  const groupsHTML = PROVIDER_GROUPS.map((g) => {
    const rows = g.providers.map((p) => {
      const cur = (cfg.integrations && cfg.integrations[p]) || {};
      const curID = cur.connection_id || "";
      const list = byProvider[p] || [];
      const opts = [
        `<option value="">Not bound</option>`,
        `<option value="default" ${curID === "default" ? "selected" : ""}>Env default</option>`,
        ...list.filter((c) => c.id !== "default").map((c) =>
          `<option value="${esc(c.id)}" ${curID === c.id ? "selected" : ""}>${esc(c.name)}</option>`),
      ].join("");
      const bindSettings = cur.settings ? JSON.stringify(cur.settings) : "";
      const needsR2 = p === "r2";
      return `
        <div class="bind-row">
          <div class="bind-head">
            <strong>${esc(PROVIDER_LABELS[p] || p)}</strong>
            <a class="muted" href="#/connections?provider=${encodeURIComponent(p)}">Manage credentials →</a>
          </div>
          <div class="form-row">
            <div class="field">
              <label>Connection</label>
              <select name="conn_${esc(p)}">${opts}</select>
              ${list.length === 0 ? `<p class="hint">No connections yet — create one under Platform → Connections.</p>` : ""}
            </div>
            ${needsR2 || bindSettings ? `
            <div class="field">
              <label>Binding settings JSON</label>
              <input name="set_${esc(p)}" value="${esc(bindSettings)}" placeholder='{"key_prefix":"…"}'>
            </div>` : `<input type="hidden" name="set_${esc(p)}" value="${esc(bindSettings)}">`}
          </div>
        </div>`;
    }).join("");
    return `<section class="bind-group">
      <h2>${esc(g.label)}</h2>
      ${rows}
    </section>`;
  }).join("");

  el.innerHTML = `
    <div class="page-head">
      <h1>Integrations</h1>
      <p class="muted">Bind platform connections to this app. Credentials live under Platform → Connections.</p>
    </div>
    <form id="put-config">
      ${groupsHTML}
      <details class="advanced">
        <summary>Advanced app settings (JSON)</summary>
        <div class="field">
          <textarea name="settings" rows="12">${esc(settingsJSON)}</textarea>
        </div>
      </details>
      <div class="form-actions">
        <button type="submit">Save integrations</button>
      </div>
    </form>`;

  el.querySelector("#put-config").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    let settings;
    try {
      settings = JSON.parse(String(fd.get("settings") || "{}"));
    } catch {
      throw new Error("settings must be valid JSON");
    }
    const integrations = {};
    for (const p of PROVIDERS) {
      const cid = String(fd.get("conn_" + p) || "");
      if (!cid) continue;
      const entry = { connection_id: cid };
      const rawSet = String(fd.get("set_" + p) || "").trim();
      if (rawSet) {
        try {
          entry.settings = JSON.parse(rawSet);
        } catch {
          throw new Error(p + " binding settings must be JSON");
        }
      }
      integrations[p] = entry;
    }
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings, integrations });
    showOk("Integrations saved");
    await renderAppConfig(el, appID);
  });
}

export { PROVIDERS, PROVIDER_GROUPS, PROVIDER_LABELS };
