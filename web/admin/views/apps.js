import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";
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

function shortRef(id) {
  if (!id) return "—";
  return id.length > 10 ? id.slice(0, 8) : id;
}

function appInitials(name, slug) {
  const src = (name || slug || "?").trim();
  if (!src) return "?";
  const parts = src.split(/[\s-_]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return src.slice(0, 2).toUpperCase();
}

function bindStamp(bound) {
  return bound
    ? `<span class="user-stamp user-stamp--active">bound</span>`
    : `<span class="user-stamp user-stamp--unknown">unbound</span>`;
}

function connectionLink(provider, connectionID, byID) {
  if (!connectionID) return `<span class="user-empty">—</span>`;
  const isDefault = connectionID === "default";
  const conn = byID.get(connectionID);
  const label = isDefault ? "Env default" : (conn?.name || shortRef(connectionID));
  let href = "#/connections?provider=" + encodeURIComponent(provider);
  if (!isDefault) href += "&edit=" + encodeURIComponent(connectionID);
  return `<a class="ov-bind-link" href="${href}" title="${esc(connectionID)}">${esc(label)}</a>`;
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
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

  applyRevealDelays(el, ".apps-row[data-reveal]");

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
  const [app, cfg, connections] = await Promise.all([
    api("GET", "/apps/" + encodeURIComponent(appID)),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/config"),
    api("GET", "/connections"),
  ]);
  const integ = cfg.integrations || {};
  const byID = new Map((connections.items || []).map((c) => [c.id, c]));
  const bound = PROVIDERS.filter((p) => integ[p] && integ[p].connection_id).length;
  const mark = appInitials(app.name, app.slug);

  let reveal = 0;
  const bindRows = PROVIDER_GROUPS.map((g) => {
    const group = `<tr class="ov-group-row"><td colspan="3">${esc(g.label)}</td></tr>`;
    const rows = g.providers.map((p) => {
      const e = integ[p] || {};
      const isBound = !!(e.connection_id);
      const i = reveal++;
      return `<tr class="ov-bind-row" data-reveal="${i}">
        <td class="ov-bind-provider">${esc(PROVIDER_LABELS[p] || p)}</td>
        <td>${bindStamp(isBound)}</td>
        <td class="ov-bind-conn">${connectionLink(p, e.connection_id, byID)}</td>
      </tr>`;
    }).join("");
    return group + rows;
  }).join("");

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · dossier</p>
        <h1 class="user-page-title">Overview</h1>
        <p class="muted">App identity and integration status.</p>
      </div>
    </div>

    <header class="user-dossier ov-dossier">
      <div class="user-dossier__rail" aria-hidden="true">APP</div>
      <div class="user-dossier__avatar">
        <span class="user-avatar user-avatar--fallback user-avatar--lg ov-mark" aria-hidden="true">${esc(mark)}</span>
      </div>
      <div class="user-dossier__body">
        <div class="user-dossier__meta-line">
          <span class="user-dossier__ref">REF ${esc(shortRef(app.id))}</span>
          ${appStatusStamp(app.status)}
        </div>
        <p class="user-dossier__title">${esc(app.name)}</p>
        <p class="user-dossier__sub">${esc(app.slug)}</p>
        <div class="user-dossier__actions">
          <a class="user-ledger-link" href="#/apps/${esc(appID)}/config">Configure integrations</a>
        </div>
      </div>
      <div class="user-dossier__balance" aria-label="Integrations bound">
        <span class="user-dossier__balance-label">Bound</span>
        <strong class="user-dossier__balance-value">${bound}<span class="ov-bound-total">/${PROVIDERS.length}</span></strong>
        <span class="user-dossier__balance-hint">integrations</span>
      </div>
    </header>

    <section class="user-meta-grid" aria-label="App identity">
      <div class="user-meta user-meta--wide">
        <span class="user-meta__k">App ID</span>
        <span class="user-meta__v user-meta__v--id">${esc(app.id)}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Slug</span>
        <span class="user-meta__v">${esc(app.slug)}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Status</span>
        <span class="user-meta__v">${appStatusStamp(app.status)}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Created</span>
        <span class="user-meta__v">${esc(fmtTime(app.created_at))}</span>
      </div>
      <div class="user-meta">
        <span class="user-meta__k">Updated</span>
        <span class="user-meta__v">${esc(fmtTime(app.updated_at))}</span>
      </div>
    </section>

    <article class="apps-create ov-edit">
      <div class="apps-create__head">
        <h2>Identity</h2>
        <span class="apps-create__badge">edit</span>
      </div>
      <form id="patch-app">
        <div class="form-row ov-identity-row">
          <div class="field field--name"><label>Name</label><input name="name" value="${esc(app.name)}" required autocomplete="off"></div>
          <div class="field field--status"><label>Status</label>
            <select name="status" id="ov-status">
              <option value="active" ${app.status === "active" ? "selected" : ""}>active</option>
              <option value="suspended" ${app.status === "suspended" ? "selected" : ""}>suspended</option>
            </select>
          </div>
        </div>
        <div class="form-actions">
          <button type="submit">Save identity</button>
        </div>
      </form>
    </article>

    <div class="user-ledger-head">
      <h2>Integrations</h2>
      <div class="user-ledger-links" aria-label="Related">
        <a class="user-ledger-link" href="#/apps/${esc(appID)}/config">Configure</a>
      </div>
    </div>
    <div class="table-wrap apps-table-wrap ov-bind-wrap">
      <table class="apps-table ov-bind-table">
        <thead><tr><th>Provider</th><th>Status</th><th>Connection</th></tr></thead>
        <tbody>${bindRows}</tbody>
      </table>
    </div>`;

  applyRevealDelays(el, ".ov-bind-row[data-reveal]");
  wireMobileSelect(el.querySelector("#ov-status"), { title: "Status" });

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
  const [cfg, connections] = await Promise.all([
    api("GET", "/apps/" + encodeURIComponent(appID) + "/config"),
    api("GET", "/connections"),
  ]);
  const byProvider = {};
  for (const c of connections.items || []) {
    (byProvider[c.provider] || (byProvider[c.provider] = [])).push(c);
  }
  const settingsJSON = JSON.stringify(cfg.settings || {}, null, 2);
  const bound = PROVIDERS.filter((p) => {
    const e = (cfg.integrations && cfg.integrations[p]) || {};
    return !!e.connection_id;
  }).length;

  let reveal = 0;
  const groupsHTML = PROVIDER_GROUPS.map((g) => {
    const rows = g.providers.map((p) => {
      const cur = (cfg.integrations && cfg.integrations[p]) || {};
      const curID = cur.connection_id || "";
      const isBound = !!curID;
      const list = byProvider[p] || [];
      const opts = [
        `<option value="">Not bound</option>`,
        `<option value="default" ${curID === "default" ? "selected" : ""}>Env default</option>`,
        ...list.filter((c) => c.id !== "default").map((c) =>
          `<option value="${esc(c.id)}" ${curID === c.id ? "selected" : ""}>${esc(c.name)}</option>`),
      ].join("");
      const bindSettings = cur.settings ? JSON.stringify(cur.settings) : "";
      const needsR2 = p === "r2";
      const i = reveal++;
      return `
        <article class="ig-bind" data-reveal="${i}">
          <div class="ig-bind__head">
            <div class="ig-bind__title">
              <strong>${esc(PROVIDER_LABELS[p] || p)}</strong>
              ${bindStamp(isBound)}
            </div>
            <a class="ig-bind__cred" href="#/connections?provider=${encodeURIComponent(p)}">Credentials</a>
          </div>
          <div class="form-row ig-bind__fields">
            <div class="field field--conn">
              <label>Connection</label>
              <select name="conn_${esc(p)}" id="conn-${esc(p)}" class="ig-conn">${opts}</select>
              ${list.length === 0 ? `<p class="hint">No connections yet — create one under Platform → Connections.</p>` : ""}
            </div>
            ${needsR2 || bindSettings ? `
            <div class="field field--settings">
              <label>Binding settings JSON</label>
              <input name="set_${esc(p)}" value="${esc(bindSettings)}" placeholder='{"key_prefix":"…"}' autocomplete="off">
            </div>` : `<input type="hidden" name="set_${esc(p)}" value="${esc(bindSettings)}">`}
          </div>
        </article>`;
    }).join("");
    return `<section class="ig-group" aria-label="${esc(g.label)}">
      <h2 class="ig-group__title">${esc(g.label)}</h2>
      <div class="ig-group__list">${rows}</div>
    </section>`;
  }).join("");

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · bindings</p>
        <h1 class="user-page-title">Integrations</h1>
        <p class="muted">Bind platform connections to this app. Credentials live under Platform → Connections.</p>
      </div>
      <div class="user-count" aria-label="Integrations bound">${bound}<span class="ov-bound-total">/${PROVIDERS.length}</span></div>
    </div>

    <form id="put-config" class="ig-form">
      ${groupsHTML}

      <div class="form-actions ig-actions">
        <button type="submit" id="save-integrations">Save integrations</button>
      </div>

      <details class="ig-advanced">
        <summary>Advanced app settings (JSON)</summary>
        <div class="field">
          <textarea id="app-settings-json" name="settings" rows="12" spellcheck="false">${esc(settingsJSON)}</textarea>
        </div>
        <div class="form-actions ig-actions">
          <button type="button" id="save-settings">Save settings only</button>
        </div>
      </details>
    </form>`;

  applyRevealDelays(el, ".ig-bind[data-reveal]");
  el.querySelectorAll("select.ig-conn").forEach((sel) => {
    const title = sel.closest(".ig-bind")?.querySelector(".ig-bind__title strong")?.textContent?.trim()
      || "Connection";
    wireMobileSelect(sel, { title });
  });

  const readSettings = () => {
    try {
      return JSON.parse(String(el.querySelector("#app-settings-json")?.value || "{}"));
    } catch {
      throw new Error("settings must be valid JSON");
    }
  };

  const readIntegrations = () => {
    const form = el.querySelector("#put-config");
    const fd = new FormData(form);
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
    return integrations;
  };

  el.querySelector("#put-config").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const settings = readSettings();
    const integrations = readIntegrations();
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings, integrations });
    showOk("Integrations saved");
    await renderAppConfig(el, appID);
  });

  el.querySelector("#save-settings").addEventListener("click", async () => {
    const settings = readSettings();
    // Omit integrations so existing bindings are preserved.
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings });
    showOk("Settings saved");
    await renderAppConfig(el, appID);
  });
}

export { PROVIDERS, PROVIDER_GROUPS, PROVIDER_LABELS };
