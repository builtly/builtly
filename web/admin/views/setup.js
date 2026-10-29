import { esc, setToken, showOk } from "../api.js";
import { showErrorToast } from "../toast.js";

async function setupFetch(method, path, body, setupToken) {
  const headers = { "Content-Type": "application/json" };
  if (setupToken) headers["X-Builtly-Setup-Token"] = setupToken;
  const init = { method, headers };
  if (body !== undefined) init.body = JSON.stringify({ ...body, setup_token: setupToken });
  const res = await fetch("/api/v1/setup" + path, init);
  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = { raw: text };
    }
  }
  if (!res.ok) {
    const msg = (data && data.error && data.error.message) || res.statusText || "request failed";
    throw new Error(msg);
  }
  return data;
}

export async function fetchSetupStatus(setupToken) {
  const headers = {};
  if (setupToken) headers["X-Builtly-Setup-Token"] = setupToken;
  const res = await fetch("/api/v1/setup/status", { headers });
  if (!res.ok) return { needs_setup: false };
  return res.json();
}

function driverNeedsDSN(driver) {
  return driver === "postgres" || driver === "libsql";
}

/**
 * @param {HTMLElement} root
 * @param {{ onConfigured: (adminToken: string) => void }} opts
 */
export async function renderSetup(root, opts) {
  const status = await fetchSetupStatus();
  if (!status.needs_setup) {
    location.hash = "#/apps";
    return;
  }

  const suggested = status.public_url || status.suggested_url || "";
  const defaultSQLite = status.default_sqlite || "file:./data/builtly.db";
  const prefDriver = status.db_driver || "sqlite";
  const prefDSN = status.db_dsn || "";
  const reason = status.reason || "unconfigured";
  const preserve = !!status.preserve_secrets;

  const reasonLede = reason === "db_unreachable"
    ? `Database unreachable${status.db_error ? `: ${esc(status.db_error)}` : ""}. Fix the connection below. Existing secrets will be kept.`
    : `Set the public URL and database. Secrets are generated on save and written under <code>${esc(status.data_dir || "./data")}</code> on this node only.`;

  root.innerHTML = `
    <div class="login-wrap">
      <div class="login-panel setup-panel" data-reveal="1">
        <aside class="login-panel__rail" aria-hidden="true">SETUP</aside>
        <div class="login-panel__body">
          <p class="user-kicker">Platform · first boot</p>
          <div class="login-brand">
            <img class="login-brand__logo" src="/admin/icon.svg" alt="" width="48" height="48">
            <div class="login-brand__text">
              <span class="login-brand__name">Builtly</span>
              <span class="login-brand__mark">setup</span>
            </div>
          </div>
          <p class="login-lede">${reasonLede}</p>
          <form id="setup-form" class="login-form setup-form">
            ${status.requires_token ? `
            <label class="login-form__label" for="setup-token">Setup token</label>
            <input id="setup-token" name="setup_token" type="text" required autocomplete="off" spellcheck="false" placeholder="From server logs (BUILTLY_SETUP_TOKEN)">
            <p class="setup-hint">Printed once when the container starts in setup mode.</p>
            ` : ""}
            <label class="login-form__label" for="public-url">Public URL</label>
            <input id="public-url" name="public_url" type="text" required value="${esc(suggested)}" placeholder="https://api.example.com" autocomplete="off" spellcheck="false">
            <p class="setup-hint">Used for OAuth callbacks and webhooks.</p>

            <fieldset class="setup-fieldset">
              <legend>Database</legend>
              <label class="setup-choice">
                <input type="radio" name="driver" value="sqlite"${prefDriver === "sqlite" ? " checked" : ""}>
                <span>SQLite — local file</span>
              </label>
              <label class="setup-choice">
                <input type="radio" name="driver" value="libsql"${prefDriver === "libsql" || prefDriver === "turso" ? " checked" : ""}>
                <span>SQLite cloud — Turso / libSQL</span>
              </label>
              <label class="setup-choice">
                <input type="radio" name="driver" value="postgres"${prefDriver === "postgres" ? " checked" : ""}>
                <span>Postgres — Neon / Supabase / self-hosted</span>
              </label>
              <div id="dsn-field" hidden>
                <label class="login-form__label" for="db-dsn" id="dsn-label">Connection string</label>
                <input id="db-dsn" name="dsn" type="text" value="${esc(prefDSN)}" placeholder="" autocomplete="off" spellcheck="false">
                <p class="setup-hint" id="dsn-hint"></p>
              </div>
              <p class="setup-hint" id="sqlite-hint">Uses <code>${esc(defaultSQLite)}</code> on the data volume. Not for multi-replica.</p>
            </fieldset>

            <div class="setup-actions">
              <button type="button" class="login-github" id="setup-test">Test connection</button>
              <button type="submit" class="login-form__submit">${preserve ? "Update &amp; reconnect" : "Save &amp; migrate"}</button>
            </div>
          </form>

          <div id="setup-done" class="setup-done" hidden>
            <p class="banner" data-tone="ok">Configured on this node. For multi-node, copy env or download the config into every machine’s Secrets — values must match.</p>
            <label class="login-form__label">Admin token</label>
            <div class="setup-copy-row">
              <code class="mono setup-admin-token" id="admin-token-out"></code>
              <button type="button" class="conn-copy" id="copy-admin">Copy</button>
            </div>
            <label class="login-form__label">Platform env (Fly Secrets etc.)</label>
            <pre class="mono setup-env" id="env-out"></pre>
            <div class="setup-actions setup-actions--start">
              <button type="button" class="login-github" id="copy-env">Copy env</button>
              <button type="button" class="login-github" id="download-json">Download builtly.json</button>
            </div>
            <button type="button" class="login-form__submit" id="setup-continue">Continue to admin</button>
          </div>
        </div>
      </div>
    </div>`;

  const panel = root.querySelector(".login-panel");
  if (panel) requestAnimationFrame(() => panel.classList.add("login-panel--in"));

  const form = root.querySelector("#setup-form");
  const dsnField = root.querySelector("#dsn-field");
  const sqliteHint = root.querySelector("#sqlite-hint");
  const dsnLabel = root.querySelector("#dsn-label");
  const dsnHint = root.querySelector("#dsn-hint");
  const dsnInput = root.querySelector("#db-dsn");

  const driverOf = () => form.querySelector('input[name="driver"]:checked')?.value || "sqlite";
  const syncDriver = () => {
    const driver = driverOf();
    const need = driverNeedsDSN(driver);
    dsnField.hidden = !need;
    sqliteHint.hidden = driver !== "sqlite";
    if (driver === "postgres") {
      dsnLabel.textContent = "Postgres connection string";
      dsnInput.placeholder = "postgres://user:pass@host:5432/db?sslmode=require";
      dsnHint.textContent = "Neon, Supabase, and self-hosted all use a normal Postgres URL.";
    } else if (driver === "libsql") {
      dsnLabel.textContent = "Turso / libSQL URL";
      dsnInput.placeholder = "libsql://your-db-org.turso.io?authToken=…";
      dsnHint.textContent = "From the Turso dashboard. Uses the SQLite migration dialect over the wire.";
    }
  };
  form.querySelectorAll('input[name="driver"]').forEach((el) => el.addEventListener("change", syncDriver));
  syncDriver();

  const tokenVal = () => root.querySelector("#setup-token")?.value.trim() || "";
  const dsnVal = () => root.querySelector("#db-dsn")?.value.trim() || "";

  // After the operator pastes the setup token, refetch status so a recovery
  // flow can prefill the real DSN (status omits it without the token).
  const tokenInput = root.querySelector("#setup-token");
  if (tokenInput && status.db_dsn_set && !prefDSN) {
    tokenInput.addEventListener("change", async () => {
      const tok = tokenVal();
      if (!tok) return;
      try {
        const st = await fetchSetupStatus(tok);
        if (st.db_dsn && dsnInput && !dsnInput.value.trim()) {
          dsnInput.value = st.db_dsn;
          if (st.db_driver) {
            const radio = form.querySelector(`input[name="driver"][value="${st.db_driver}"]`);
            if (radio) {
              radio.checked = true;
              syncDriver();
            }
          }
        }
      } catch {
        /* ignore */
      }
    });
  }

  const payloadFor = () => {
    const driver = driverOf();
    return {
      driver,
      dsn: driverNeedsDSN(driver) ? dsnVal() : "",
    };
  };

  root.querySelector("#setup-test").addEventListener("click", async () => {
    try {
      await setupFetch("POST", "/test-db", payloadFor(), tokenVal());
      showOk("Database reachable");
    } catch (e) {
      showErrorToast({ message: e.message || String(e) });
    }
  });

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    try {
      const data = await setupFetch("POST", "/complete", {
        public_url: form.querySelector("#public-url").value.trim(),
        ...payloadFor(),
      }, tokenVal());
      form.hidden = true;
      const done = root.querySelector("#setup-done");
      done.hidden = false;
      root.querySelector("#admin-token-out").textContent = data.admin_token || "";
      const envLines = Object.entries(data.env || {}).map(([k, v]) => `${k}=${v}`).join("\n");
      root.querySelector("#env-out").textContent = envLines;

      root.querySelector("#copy-admin").onclick = async () => {
        try {
          await navigator.clipboard.writeText(data.admin_token);
          showOk("Admin token copied");
        } catch {
          showOk(data.admin_token);
        }
      };
      root.querySelector("#copy-env").onclick = async () => {
        try {
          await navigator.clipboard.writeText(envLines);
          showOk("Env block copied");
        } catch {
          showOk(envLines);
        }
      };
      root.querySelector("#download-json").onclick = () => {
        const blob = new Blob([data.config_json || "{}"], { type: "application/json" });
        const a = document.createElement("a");
        a.href = URL.createObjectURL(blob);
        a.download = "builtly.json";
        a.click();
        URL.revokeObjectURL(a.href);
        showOk("builtly.json downloaded");
      };
      root.querySelector("#setup-continue").onclick = async () => {
        if (data.admin_token) setToken(data.admin_token);
        const btn = root.querySelector("#setup-continue");
        btn.disabled = true;
        btn.textContent = "Starting…";
        for (let i = 0; i < 30; i++) {
          try {
            const st = await fetchSetupStatus();
            if (!st.needs_setup) break;
          } catch {
            /* server reloading */
          }
          await new Promise((r) => setTimeout(r, 400));
        }
        opts.onConfigured(data.admin_token || "");
      };
    } catch (e) {
      showErrorToast({ message: e.message || String(e) });
    }
  });
}
