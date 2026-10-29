import { api, esc, showOk } from "../api.js";

/** @typedef {{ key: string, label: string, type: string, required?: boolean, placeholder?: string, hint?: string, rows?: number, section?: string }} FieldDef */

/** @type {Record<string, { label: string, settings: FieldDef[], secrets: FieldDef[] }>} */
const SCHEMAS = {
  stripe: {
    label: "Stripe",
    settings: [
      { key: "livemode", label: "Live mode", type: "checkbox", hint: "Must match sk_live_ / sk_test_. account_id is filled automatically." },
    ],
    secrets: [
      { key: "secret_key", label: "Secret key", type: "password", required: true, placeholder: "sk_test_… or sk_live_…" },
      { key: "webhook_secrets", label: "Webhook secrets", type: "lines", required: true, placeholder: "whsec_… (one per line)", hint: "One signing secret per line." },
    ],
  },
  resend: {
    label: "Resend",
    settings: [],
    secrets: [
      { key: "api_key", label: "API key", type: "password", required: true, placeholder: "re_…" },
    ],
  },
  r2: {
    label: "Cloudflare R2",
    settings: [
      { key: "account_id", label: "Account ID", type: "text", required: true },
      { key: "bucket", label: "Bucket", type: "text", required: true },
      { key: "public_base_url", label: "Public base URL", type: "text", placeholder: "https://cdn.example.com (optional)" },
    ],
    secrets: [
      { key: "access_key_id", label: "Access key ID", type: "text", required: true },
      { key: "secret_access_key", label: "Secret access key", type: "password", required: true },
    ],
  },
  google_oauth: {
    label: "Google OAuth",
    settings: [
      {
        key: "client_id", label: "Web client ID", type: "text", required: true, section: "Web",
        placeholder: "….apps.googleusercontent.com",
        hint: "Google Cloud → APIs & Services → Credentials → Web application.",
      },
      {
        key: "ios_client_id", label: "iOS client ID", type: "text", section: "iOS",
        placeholder: "….apps.googleusercontent.com",
        hint: "iOS OAuth client (bundle ID). No client secret.",
      },
      {
        key: "android_client_id", label: "Android client ID", type: "text", section: "Android",
        placeholder: "….apps.googleusercontent.com",
        hint: "Android OAuth client (package + SHA-1). No client secret.",
      },
    ],
    secrets: [
      {
        key: "client_secret", label: "Web client secret", type: "password", required: true, section: "Web",
        hint: "Pairs with the Web client ID. Native (iOS/Android) clients do not use a secret.",
      },
    ],
  },
  github_oauth: {
    label: "GitHub OAuth",
    settings: [
      { key: "client_id", label: "Client ID", type: "text", required: true },
    ],
    secrets: [
      { key: "client_secret", label: "Client secret", type: "password", required: true },
    ],
  },
  apple_signin: {
    label: "Apple Sign In",
    settings: [
      { key: "team_id", label: "Team ID", type: "text", required: true },
      { key: "key_id", label: "Key ID", type: "text", required: true },
      { key: "services_id", label: "Services ID (web)", type: "text", required: true },
      { key: "bundle_ids", label: "Bundle IDs (native)", type: "lines", placeholder: "com.example.app" },
    ],
    secrets: [
      { key: "private_key_pem", label: "Private key (PEM)", type: "textarea", required: true, rows: 6, placeholder: "-----BEGIN PRIVATE KEY-----" },
    ],
  },
  apple_iap: {
    label: "Apple IAP",
    settings: [
      { key: "issuer_id", label: "Issuer ID", type: "text", required: true },
      { key: "key_id", label: "Key ID", type: "text", required: true },
      { key: "bundle_id", label: "Bundle ID", type: "text", required: true },
    ],
    secrets: [
      { key: "private_key_pem", label: "Private key (PEM)", type: "textarea", required: true, rows: 6, placeholder: "-----BEGIN PRIVATE KEY-----" },
    ],
  },
  google_play: {
    label: "Google Play",
    settings: [
      { key: "package_name", label: "Package name", type: "text", required: true, placeholder: "com.example.app" },
    ],
    secrets: [
      { key: "service_account_json", label: "Service account JSON", type: "textarea", required: true, rows: 8, placeholder: "{ … }" },
    ],
  },
  posthog: {
    label: "PostHog",
    settings: [
      { key: "host", label: "Host", type: "text", required: true, placeholder: "https://us.i.posthog.com" },
    ],
    secrets: [
      { key: "project_api_key", label: "Project API key", type: "password", required: true, placeholder: "phc_…" },
    ],
  },
  promotekit: {
    label: "PromoteKit",
    settings: [],
    secrets: [
      { key: "api_key", label: "API key", type: "password", required: true },
    ],
  },
  affonso: {
    label: "Affonso",
    settings: [],
    secrets: [
      { key: "api_key", label: "API key", type: "password", required: true },
    ],
  },
};

const PROVIDER_GROUPS = [
  { id: "auth", label: "Auth", providers: ["google_oauth", "github_oauth", "apple_signin"] },
  { id: "payments", label: "Payments", providers: ["stripe", "apple_iap", "google_play"] },
  { id: "email", label: "Email", providers: ["resend"] },
  { id: "storage", label: "Storage", providers: ["r2"] },
  { id: "analytics", label: "Analytics", providers: ["posthog"] },
  { id: "affiliates", label: "Affiliates", providers: ["promotekit", "affonso"] },
];

const PROVIDERS = PROVIDER_GROUPS.flatMap((g) => g.providers);

function linesToArray(raw) {
  return String(raw || "")
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean);
}

function arrayToLines(v) {
  if (Array.isArray(v)) return v.join("\n");
  if (v == null || v === "") return "";
  return String(v);
}

function renderField(f, value, { prefix, forEdit }) {
  const name = prefix + f.key;
  const req = f.required && !forEdit ? "required" : "";
  const ph = f.placeholder ? ` placeholder="${esc(f.placeholder)}"` : "";
  const hint = f.hint ? `<p class="hint">${esc(f.hint)}</p>` : "";
  if (f.type === "checkbox") {
    const checked = value === true || value === "true" || value === "on" ? "checked" : "";
    return `<div class="field">
      <label class="checkbox"><input type="checkbox" name="${esc(name)}" ${checked}> ${esc(f.label)}</label>
      ${hint}
    </div>`;
  }
  if (f.type === "lines" || f.type === "textarea") {
    const rows = f.rows || (f.type === "lines" ? 3 : 5);
    const keep = forEdit && prefix === "sec_"
      ? `<p class="hint">Leave blank to keep existing.</p>`
      : hint;
    return `<div class="field">
      <label for="${esc(name)}">${esc(f.label)}</label>
      <textarea id="${esc(name)}" name="${esc(name)}" rows="${rows}" ${req}${ph}>${esc(arrayToLines(value))}</textarea>
      ${keep}
    </div>`;
  }
  const inputType = f.type === "password" ? "password" : "text";
  const keep = forEdit && prefix === "sec_"
    ? `<p class="hint">Leave blank to keep existing.</p>`
    : hint;
  return `<div class="field">
    <label for="${esc(name)}">${esc(f.label)}</label>
    <input id="${esc(name)}" type="${inputType}" name="${esc(name)}" value="${esc(value == null ? "" : String(value))}" ${req}${ph} autocomplete="off">
    ${keep}
  </div>`;
}

function collectPayload(form, schema, { requireSecrets }) {
  const fd = new FormData(form);
  const name = String(fd.get("name") || "").trim();
  const settings = {};
  for (const f of schema.settings) {
    if (f.type === "checkbox") {
      settings[f.key] = fd.get("set_" + f.key) === "on";
      continue;
    }
    const raw = String(fd.get("set_" + f.key) || "").trim();
    if (f.type === "lines") {
      const arr = linesToArray(raw);
      if (arr.length) settings[f.key] = arr;
      else if (f.required && requireSecrets) throw new Error(f.label + " is required");
      continue;
    }
    if (raw) settings[f.key] = raw;
    else if (f.required && requireSecrets) throw new Error(f.label + " is required");
  }
  // Keep native_client_ids in sync for Google (resolver merges ios/android + this list).
  if (schema === SCHEMAS.google_oauth) {
    const natives = [settings.ios_client_id, settings.android_client_id].filter(Boolean);
    if (natives.length) settings.native_client_ids = natives;
    else delete settings.native_client_ids;
  }
  const secrets = {};
  let hasSecret = false;
  for (const f of schema.secrets) {
    const raw = String(fd.get("sec_" + f.key) || "").trim();
    if (!raw) {
      if (requireSecrets && f.required) throw new Error(f.label + " is required");
      continue;
    }
    hasSecret = true;
    if (f.type === "lines") secrets[f.key] = linesToArray(raw);
    else secrets[f.key] = raw;
  }
  return { name, settings, secrets, hasSecret };
}

/** Prefer ios_/android_ fields; fall back to legacy native_client_ids[] for edit forms. */
function normalizeGoogleSettings(st) {
  const out = { ...(st || {}) };
  const natives = Array.isArray(out.native_client_ids) ? out.native_client_ids.filter(Boolean) : [];
  if (!out.ios_client_id && natives[0]) out.ios_client_id = natives[0];
  if (!out.android_client_id && natives[1]) out.android_client_id = natives[1];
  return out;
}

function sectionOrder(schema) {
  const order = [];
  const seen = new Set();
  for (const f of [...schema.settings, ...schema.secrets]) {
    const s = f.section || "Settings";
    if (!seen.has(s)) {
      seen.add(s);
      order.push(s);
    }
  }
  return order;
}

/** Short primary setting for the list column. */
function primarySetting(provider, settings) {
  const s = settings || {};
  switch (provider) {
    case "stripe":
      return s.livemode ? "live" : "test";
    case "r2":
      return s.bucket || s.account_id || "—";
    case "google_oauth": {
      const parts = [];
      if (s.client_id) parts.push("web");
      if (s.ios_client_id || (Array.isArray(s.native_client_ids) && s.native_client_ids[0])) parts.push("ios");
      if (s.android_client_id || (Array.isArray(s.native_client_ids) && s.native_client_ids[1])) parts.push("android");
      return parts.length ? parts.join(" · ") : (s.client_id || "—");
    }
    case "github_oauth":
      return s.client_id || "—";
    case "apple_signin":
      return s.services_id || s.team_id || "—";
    case "apple_iap":
      return s.bundle_id || "—";
    case "google_play":
      return s.package_name || "—";
    case "posthog":
      return s.host || "—";
    default:
      return "—";
  }
}

function secretsHint(secrets) {
  if (!secrets || typeof secrets !== "object") return "—";
  const keys = Object.keys(secrets);
  if (!keys.length) return "—";
  return keys.map((k) => `${k} ${secrets[k]}`).join(", ");
}

function shortRef(id) {
  if (!id) return "—";
  return id.length > 10 ? id.slice(0, 8) : id;
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

function providerNav(active) {
  // Use a plain div — Pico's `nav`/`ul` defaults are horizontal flex and break this rail.
  return `<div class="conn-nav" role="navigation" aria-label="Providers">
    ${PROVIDER_GROUPS.map((g) => `
      <div class="conn-nav-group">
        <div class="conn-nav-label">${esc(g.label)}</div>
        ${g.providers.map((p) => {
          const cur = p === active ? ' aria-current="page"' : "";
          return `<a class="conn-nav__link" href="#/connections?provider=${encodeURIComponent(p)}"${cur}>${esc(SCHEMAS[p].label)}</a>`;
        }).join("")}
      </div>`).join("")}
  </div>`;
}

function formMarkup(schema, { mode, values }) {
  const forEdit = mode === "edit";
  let st = values || {};
  if (schema === SCHEMAS.google_oauth) st = normalizeGoogleSettings(st);

  const useSections = [...schema.settings, ...schema.secrets].some((f) => f.section);
  let blocks = "";
  if (useSections) {
    for (const section of sectionOrder(schema)) {
      const setFields = schema.settings.filter((f) => (f.section || "Settings") === section);
      const secFields = schema.secrets.filter((f) => (f.section || "Settings") === section);
      if (!setFields.length && !secFields.length) continue;
      const secretNote = secFields.length
        ? (forEdit
          ? `<p class="hint">Secrets optional — leave blank to keep existing.</p>`
          : `<p class="hint">Write-only; never shown again after save.</p>`)
        : "";
      blocks += `<fieldset class="conn-fieldset">
        <legend>${esc(section)}</legend>
        ${setFields.map((f) => renderField(f, st[f.key], { prefix: "set_", forEdit })).join("")}
        ${secretNote}
        ${secFields.map((f) => renderField(f, "", { prefix: "sec_", forEdit })).join("")}
      </fieldset>`;
    }
  } else {
    if (schema.settings.length) {
      blocks += `<fieldset class="conn-fieldset">
        <legend>Settings</legend>
        ${schema.settings.map((f) => renderField(f, st[f.key], { prefix: "set_", forEdit })).join("")}
      </fieldset>`;
    }
    if (schema.secrets.length) {
      blocks += `<fieldset class="conn-fieldset">
        <legend>${forEdit ? "Rotate secrets" : "Secrets"}</legend>
        ${forEdit ? `<p class="hint">Optional — leave blank to keep existing values.</p>` : `<p class="hint">Write-only; never shown again after save.</p>`}
        ${schema.secrets.map((f) => renderField(f, "", { prefix: "sec_", forEdit })).join("")}
      </fieldset>`;
    }
  }

  return `
    <div class="field">
      <label for="conn-name">Name</label>
      <input id="conn-name" name="name" required value="${esc(st.name || "")}" placeholder="${esc(schema.label)} production" autocomplete="off">
    </div>
    ${blocks}
    <div class="form-actions conn-form-actions">
      <button type="button" class="secondary cancel-drawer">Cancel</button>
      <button type="submit">${forEdit ? "Save changes" : "Create connection"}</button>
    </div>`;
}

function closeDrawer(el) {
  const drawer = el.querySelector("#conn-drawer");
  if (!drawer) return;
  drawer.hidden = true;
  drawer.innerHTML = "";
  el.querySelector("#conn-add")?.removeAttribute("disabled");
  const empty = el.querySelector(".conn-empty");
  if (empty) empty.hidden = false;
}

function openDrawer(el, title, bodyHTML) {
  const drawer = el.querySelector("#conn-drawer");
  drawer.innerHTML = `
    <div class="conn-drawer-head">
      <div>
        <p class="user-kicker">Connection</p>
        <h3>${esc(title)}</h3>
      </div>
      <button type="button" class="secondary cancel-drawer conn-drawer__close" aria-label="Close">✕</button>
    </div>
    ${bodyHTML}`;
  drawer.hidden = false;
  el.querySelector("#conn-add")?.setAttribute("disabled", "disabled");
  const empty = el.querySelector(".conn-empty");
  if (empty) empty.hidden = true;
  drawer.scrollIntoView({ behavior: "smooth", block: "nearest" });
  drawer.querySelectorAll(".cancel-drawer").forEach((btn) => {
    btn.addEventListener("click", () => closeDrawer(el));
  });
}

export async function renderConnections(el) {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  let provider = params.get("provider") || "";
  const editID = params.get("edit") || "";
  if (!provider || !SCHEMAS[provider]) {
    provider = PROVIDERS[0];
    history.replaceState(null, "", "#/connections?provider=" + encodeURIComponent(provider));
  }
  const schema = SCHEMAS[provider];
  const group = PROVIDER_GROUPS.find((g) => g.providers.includes(provider));

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">Platform · credentials</p>
        <h1 class="user-page-title">Connections</h1>
        <p class="muted">Encrypted provider credentials shared across apps.</p>
      </div>
    </div>
    <div class="conn-layout">
      ${providerNav(provider)}
      <div class="conn-panel"><p class="muted">Loading…</p></div>
    </div>`;

  const data = await api("GET", "/connections?provider=" + encodeURIComponent(provider));
  const items = data.items || [];

  const rows = items.map((c, i) => {
    const usedN = (c.used_by_apps || []).length;
    const used = usedN ? `${usedN} app${usedN === 1 ? "" : "s"}` : "";
    const ro = c.read_only;
    const actions = ro
      ? `<span class="user-stamp user-stamp--unknown">env</span>`
      : `<div class="conn-row-actions">
          <button type="button" class="conn-edit edit-conn" data-id="${esc(c.id)}">Edit</button>
          <button type="button" class="conn-del del-conn" data-id="${esc(c.id)}">Delete</button>
        </div>`;
    const webhook = c.webhook_url
      ? `<button type="button" class="conn-copy copy-url" data-url="${esc(c.webhook_url)}" title="${esc(c.webhook_url)}">Copy URL</button>`
      : `<span class="user-empty">—</span>`;
    return `<tr class="conn-row" data-conn-id="${esc(c.id)}" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <div class="conn-identity">
          <span class="conn-identity__name">${esc(c.name)}</span>
          <span class="conn-identity__id mono" title="${esc(c.id)}">${esc(shortRef(c.id))}</span>
        </div>
      </td>
      <td class="conn-config">${esc(primarySetting(provider, c.settings))}</td>
      <td class="conn-secrets mono">${esc(secretsHint(c.secrets))}</td>
      <td class="conn-webhook">${webhook}</td>
      <td class="conn-used">${used ? esc(used) : `<span class="user-empty">—</span>`}</td>
      <td class="conn-actions">${actions}</td>
    </tr>`;
  }).join("");

  el.querySelector(".conn-panel").innerHTML = `
    <div class="conn-toolbar">
      <div>
        <p class="user-kicker">${esc(group ? group.label : "Provider")}</p>
        <div class="conn-title">${esc(schema.label)}</div>
        <p class="muted">${items.length} connection${items.length === 1 ? "" : "s"}</p>
      </div>
      <div class="conn-toolbar__actions">
        <div class="user-count" aria-label="Connections">${items.length}</div>
        <button type="button" class="conn-create" id="conn-add">Add connection</button>
      </div>
    </div>

    <div id="conn-drawer" class="conn-drawer" hidden></div>

    ${items.length
      ? `<div class="table-wrap apps-table-wrap conn-table-wrap">
          <table class="apps-table conn-table">
            <thead>
              <tr>
                <th class="user-idx-h"></th>
                <th>Name</th>
                <th>Config</th>
                <th>Secrets</th>
                <th>Webhook</th>
                <th>Used by</th>
                <th></th>
              </tr>
            </thead>
            <tbody>${rows}</tbody>
          </table>
        </div>`
      : `<div class="conn-empty">
          <p>No ${esc(schema.label)} connections yet.</p>
          <button type="button" class="conn-create" id="conn-add-empty">Add the first one</button>
        </div>`}`;

  applyRevealDelays(el, ".conn-row[data-reveal]");

  const openCreate = () => {
    openDrawer(el, `Add ${schema.label}`, `<form id="create-conn">${formMarkup(schema, { mode: "create", values: {} })}</form>`);
    el.querySelector("#create-conn").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const { name, settings, secrets } = collectPayload(ev.target, schema, { requireSecrets: true });
      await api("POST", "/connections", { provider, name, settings, secrets });
      showOk("Connection created");
      await renderConnections(el);
    });
  };

  el.querySelector("#conn-add")?.addEventListener("click", openCreate);
  el.querySelector("#conn-add-empty")?.addEventListener("click", openCreate);

  el.querySelectorAll(".edit-conn").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const c = await api("GET", "/connections/" + encodeURIComponent(btn.dataset.id));
      openDrawer(
        el,
        `Edit ${c.name}`,
        `<form id="edit-conn">${formMarkup(schema, { mode: "edit", values: { name: c.name, ...(c.settings || {}) } })}</form>`,
      );
      el.querySelector("#edit-conn").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const { name, settings, secrets, hasSecret } = collectPayload(ev.target, schema, { requireSecrets: false });
        const merged = { ...(c.settings || {}) };
        for (const f of schema.settings) {
          if (Object.prototype.hasOwnProperty.call(settings, f.key)) merged[f.key] = settings[f.key];
          else delete merged[f.key];
        }
        const body = { name, settings: merged };
        if (hasSecret) body.secrets = secrets;
        await api("PATCH", "/connections/" + encodeURIComponent(btn.dataset.id), body);
        showOk("Connection updated");
        await renderConnections(el);
      });
    });
  });

  el.querySelectorAll(".del-conn").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm("Delete this connection?")) return;
      await api("DELETE", "/connections/" + encodeURIComponent(btn.dataset.id));
      showOk("Deleted");
      await renderConnections(el);
    });
  });

  el.querySelectorAll(".copy-url").forEach((btn) => {
    btn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(btn.dataset.url);
        showOk("Webhook URL copied");
      } catch {
        showOk(btn.dataset.url);
      }
    });
  });

  if (editID) {
    const editBtn = el.querySelector(`.edit-conn[data-id="${CSS.escape(editID)}"]`);
    if (editBtn) editBtn.click();
  }
}
