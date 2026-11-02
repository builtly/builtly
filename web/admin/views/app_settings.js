import { api, esc, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";
import { showErrorToast } from "../toast.js";

const DEFAULT_MIME_TYPES = [
  "image/png",
  "image/jpeg",
  "image/webp",
  "application/pdf",
];

const BYTES_PER_MB = 1024 * 1024;

export async function renderAppSettings(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>App Settings</h1><p class="muted">Loading…</p></div>`;

  const [app, cfg] = await Promise.all([
    api("GET", "/apps/" + encodeURIComponent(appID)),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/config"),
  ]);

  const s = cfg.settings || {};
  const auth = s.auth || {};
  const authProviders = new Set(auth.providers || []);
  const storage = s.storage || {};
  const referral = s.referral || {};
  const affiliate = s.affiliate || {};
  const analytics = s.analytics || {};

  const allowedOriginsText = (s.allowed_origins || []).join("\n");
  const redirectURLsText = (s.auth_redirect_urls || []).join("\n");
  const contentTypesText = (storage.allowed_content_types || DEFAULT_MIME_TYPES).join("\n");

  const maxFileMB = Math.round((storage.max_file_bytes || 10 * BYTES_PER_MB) / BYTES_PER_MB);
  const userQuotaMB = Math.round((storage.user_quota_bytes || 100 * BYTES_PER_MB) / BYTES_PER_MB);
  const rawJSON = JSON.stringify(s, null, 2);

  el.innerHTML = `
    <p class="user-back"><a href="#/apps/${esc(appID)}">← Back to Overview</a></p>

    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · ${esc(app.slug || app.name)} · settings</p>
        <h1 class="user-page-title">App Settings</h1>
        <p class="muted">Structured application configuration for CORS, auth policies, storage limits, and referrals.</p>
      </div>
    </div>

    <form id="settings-form" class="settings-form">
      <!-- Section 1: Network & Security -->
      <section class="settings-card" aria-label="Network & Security">
        <h2 class="settings-card__title">Network &amp; Routing</h2>
        <div class="form-row">
          <div class="field field--full">
            <label for="allowed-origins">Allowed Origins (CORS)</label>
            <textarea id="allowed-origins" name="allowed_origins" class="settings-mono-textarea" rows="3" spellcheck="false" placeholder="https://app.example.com&#10;https://staging.example.com">${esc(allowedOriginsText)}</textarea>
            <p class="hint">Allowed origins for browser clients. One URL per line. Wildcards are forbidden.</p>
          </div>
          <div class="field field--full">
            <label for="auth-redirect-urls">Auth Redirect URLs</label>
            <textarea id="auth-redirect-urls" name="auth_redirect_urls" class="settings-mono-textarea" rows="3" spellcheck="false" placeholder="https://app.example.com/auth/callback&#10;myapp://auth/callback">${esc(redirectURLsText)}</textarea>
            <p class="hint">Allowed OAuth redirect URLs and mobile deep links. One URI per line.</p>
          </div>
        </div>
      </section>

      <!-- Section 2: Authentication & Access -->
      <section class="settings-card" aria-label="Authentication & Access">
        <h2 class="settings-card__title">Authentication &amp; Access</h2>

        <div class="field">
          <label>Sign-in Methods</label>
          <div class="settings-check-group">
            <label class="settings-check">
              <input type="checkbox" name="auth_password_enabled" ${auth.password_enabled !== false ? "checked" : ""}>
              <span>Enable password login</span>
            </label>
            <label class="settings-check">
              <input type="checkbox" id="auth-guest-login-enabled" name="auth_guest_login_enabled" ${auth.guest_login_enabled ? "checked" : ""}>
              <span>Enable guest email login</span>
            </label>
            <label class="settings-check">
              <input type="checkbox" name="auth_require_email_verification" ${auth.require_email_verification ? "checked" : ""}>
              <span>Require email verification</span>
            </label>
          </div>
        </div>

        <div class="field field--full" id="guest-email-domain-field">
          <label for="guest-email-domain">Guest Email Domain</label>
          <input type="text" id="guest-email-domain" name="auth_guest_email_domain" placeholder="device.example.com" value="${esc(auth.guest_email_domain || '')}" ${auth.guest_login_enabled ? "" : "disabled"}>
          <p class="hint">Dedicated domain for guest/device accounts (e.g. device.example.com). Enabled only when guest email login is checked. Guest accounts with this domain do not receive registration bonuses and can be migrated via transfer-guest-credits.</p>
        </div>

        <div class="field">
          <label>Active OAuth Providers</label>
          <div class="settings-check-group settings-check-group--inline">
            <label class="settings-check">
              <input type="checkbox" name="auth_provider_google" ${authProviders.has("google") ? "checked" : ""}>
              <span>Google</span>
            </label>
            <label class="settings-check">
              <input type="checkbox" name="auth_provider_github" ${authProviders.has("github") ? "checked" : ""}>
              <span>GitHub</span>
            </label>
            <label class="settings-check">
              <input type="checkbox" name="auth_provider_apple" ${authProviders.has("apple") ? "checked" : ""}>
              <span>Apple</span>
            </label>
          </div>
          <p class="hint">OAuth providers enabled for user sign-in.</p>
        </div>

        <div class="field field--bonus">
          <label for="signup-bonus-credits">Signup Bonus Credits</label>
          <input type="number" id="signup-bonus-credits" name="signup_bonus_credits" min="0" value="${esc(s.signup_bonus_credits || 0)}">
          <p class="hint">Initial credits granted upon new user registration.</p>
        </div>
      </section>

      <!-- Section 3: Storage Limits -->
      <section class="settings-card" aria-label="Storage Limits">
        <h2 class="settings-card__title">Storage Limits (Cloudflare R2)</h2>
        <div class="settings-grid-2">
          <div class="field">
            <label for="max-file-mb">Max File Size (MB)</label>
            <input type="number" id="max-file-mb" name="max_file_mb" min="1" max="1024" value="${esc(maxFileMB)}">
            <p class="hint">Maximum allowed size per uploaded file.</p>
          </div>
          <div class="field">
            <label for="user-quota-mb">User Quota (MB)</label>
            <input type="number" id="user-quota-mb" name="user_quota_mb" min="1" max="102400" value="${esc(userQuotaMB)}">
            <p class="hint">Total cumulative storage quota per user.</p>
          </div>
        </div>
        <div class="form-row">
          <div class="field field--full">
            <label for="allowed-content-types">Allowed Content Types</label>
            <textarea id="allowed-content-types" name="allowed_content_types" class="settings-mono-textarea" rows="3" spellcheck="false" placeholder="image/png&#10;image/jpeg&#10;image/webp&#10;application/pdf">${esc(contentTypesText)}</textarea>
            <p class="hint">Permitted MIME types, one per line or comma-separated.</p>
          </div>
        </div>
      </section>

      <!-- Section 4: Referral & Growth -->
      <section class="settings-card" aria-label="Referral & Growth">
        <h2 class="settings-card__title">Referral &amp; Growth</h2>
        <div class="field">
          <label>Program Status</label>
          <div class="settings-check-group">
            <label class="settings-check">
              <input type="checkbox" name="referral_enabled" ${referral.enabled ? "checked" : ""}>
              <span>Enable built-in referral rewards program</span>
            </label>
          </div>
        </div>

        <div class="settings-grid-3">
          <div class="field">
            <label for="referral-trigger">Reward Trigger</label>
            <select id="referral-trigger" name="referral_trigger">
              <option value="first_purchase" ${referral.trigger !== "every_purchase" ? "selected" : ""}>first_purchase (First purchase only)</option>
              <option value="every_purchase" ${referral.trigger === "every_purchase" ? "selected" : ""}>every_purchase (Every purchase)</option>
            </select>
            <p class="hint">When referral rewards trigger.</p>
          </div>
          <div class="field">
            <label for="referral-reward-type">Reward Type</label>
            <select id="referral-reward-type" name="referral_reward_type">
              <option value="percent" ${referral.reward_type !== "fixed" ? "selected" : ""}>percent (Percentage cut)</option>
              <option value="fixed" ${referral.reward_type === "fixed" ? "selected" : ""}>fixed (Fixed credits)</option>
            </select>
            <p class="hint">Percentage cut or fixed amount.</p>
          </div>
          <div class="field">
            <label for="referral-reward-val">Reward Value</label>
            <input type="number" id="referral-reward-val" name="referral_reward_value" min="1" value="${esc(referral.reward_value ?? 10)}">
            <p class="hint">Percent (1–100) or credit count.</p>
          </div>
        </div>

        <div class="settings-grid-3">
          <div class="field">
            <label for="referee-bonus">Referee Bonus Credits</label>
            <input type="number" id="referee-bonus" name="referee_bonus_credits" min="0" value="${esc(referral.referee_bonus_credits || 0)}">
            <p class="hint">Credits granted to invited user.</p>
          </div>
          <div class="field">
            <label for="apply-window-days">Attribution Window (Days)</label>
            <input type="number" id="apply-window-days" name="apply_window_days" min="0" value="${esc(referral.apply_window_days ?? 7)}">
            <p class="hint">Days from signup for eligibility.</p>
          </div>
          <div class="field">
            <label for="affiliate-provider">External Affiliate Provider</label>
            <select id="affiliate-provider" name="affiliate_provider">
              <option value="none" ${(!affiliate.provider || affiliate.provider === "none") ? "selected" : ""}>none (Disabled)</option>
              <option value="promotekit" ${affiliate.provider === "promotekit" ? "selected" : ""}>promotekit</option>
              <option value="affonso" ${affiliate.provider === "affonso" ? "selected" : ""}>affonso</option>
            </select>
            <p class="hint">Third-party affiliate tracker.</p>
          </div>
        </div>
      </section>

      <!-- Section 5: Platform & Telemetry -->
      <section class="settings-card" aria-label="Platform & Telemetry">
        <h2 class="settings-card__title">Platform &amp; Telemetry</h2>
        <div class="field">
          <label>Runtime Switches</label>
          <div class="settings-check-group">
            <label class="settings-check">
              <input type="checkbox" name="allow_iap_sandbox" ${s.allow_iap_sandbox ? "checked" : ""}>
              <span>Allow Apple IAP sandbox receipts</span>
            </label>
            <label class="settings-check">
              <input type="checkbox" name="analytics_capture_consume" ${analytics.capture_consume_events ? "checked" : ""}>
              <span>Capture credit consumption to PostHog</span>
            </label>
          </div>
        </div>
      </section>

      <div class="form-actions settings-actions">
        <button type="submit" id="save-structured-settings">Save settings</button>
      </div>

      <!-- Section 6: Advanced Raw JSON -->
      <details class="ig-advanced">
        <summary>Raw Settings JSON (Advanced / Export)</summary>
        <div class="field">
          <textarea id="app-settings-json" name="raw_settings" rows="12" spellcheck="false">${esc(rawJSON)}</textarea>
          <p class="hint">Direct JSON payload. You can paste custom configuration here or use it as an escape hatch.</p>
        </div>
        <div class="form-actions ig-actions">
          <button type="button" id="save-raw-settings">Save raw JSON</button>
        </div>
      </details>
    </form>`;

  el.querySelectorAll("select").forEach((sel) => {
    const label = sel.closest(".field")?.querySelector("label")?.textContent?.trim() || "Select";
    wireMobileSelect(sel, { title: label });
  });

  const parseLines = (text) =>
    String(text || "")
      .split(/[\r\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean);

  const collectStructuredSettings = (form) => {
    const fd = new FormData(form);

    const origins = String(fd.get("allowed_origins") || "")
      .split(/[\r\n]+/)
      .map((s) => s.trim())
      .filter(Boolean);

    const redirects = String(fd.get("auth_redirect_urls") || "")
      .split(/[\r\n]+/)
      .map((s) => s.trim())
      .filter(Boolean);

    const contentTypes = parseLines(String(fd.get("allowed_content_types") || ""));

    const providers = [];
    if (fd.get("auth_provider_google") === "on") providers.push("google");
    if (fd.get("auth_provider_github") === "on") providers.push("github");
    if (fd.get("auth_provider_apple") === "on") providers.push("apple");

    const maxMB = Math.max(1, Number(fd.get("max_file_mb")) || 10);
    const quotaMB = Math.max(1, Number(fd.get("user_quota_mb")) || 100);

    const guestLoginEnabled = fd.get("auth_guest_login_enabled") === "on";
    const guestDomainInput = form.querySelector("#guest-email-domain");
    const guestEmailDomain = guestDomainInput ? guestDomainInput.value.trim().toLowerCase() : "";

    return {
      ...(s || {}),
      allowed_origins: origins,
      auth_redirect_urls: redirects,
      signup_bonus_credits: Math.max(0, Number(fd.get("signup_bonus_credits")) || 0),
      allow_iap_sandbox: fd.get("allow_iap_sandbox") === "on",
      auth: {
        ...(s.auth || {}),
        password_enabled: fd.get("auth_password_enabled") === "on",
        guest_login_enabled: guestLoginEnabled,
        require_email_verification: fd.get("auth_require_email_verification") === "on",
        guest_email_domain: guestEmailDomain,
        providers,
      },
      storage: {
        ...(s.storage || {}),
        max_file_bytes: maxMB * BYTES_PER_MB,
        user_quota_bytes: quotaMB * BYTES_PER_MB,
        allowed_content_types: contentTypes.length ? contentTypes : DEFAULT_MIME_TYPES,
      },
      referral: {
        ...(s.referral || {}),
        enabled: fd.get("referral_enabled") === "on",
        trigger: String(fd.get("referral_trigger") || "first_purchase"),
        reward_type: String(fd.get("referral_reward_type") || "percent"),
        reward_value: Number(fd.get("referral_reward_value")) || 10,
        referee_bonus_credits: Math.max(0, Number(fd.get("referee_bonus_credits")) || 0),
        apply_window_days: Math.max(0, Number(fd.get("apply_window_days")) || 7),
      },
      affiliate: {
        ...(s.affiliate || {}),
        provider: String(fd.get("affiliate_provider") || "none"),
      },
      analytics: {
        ...(s.analytics || {}),
        capture_consume_events: fd.get("analytics_capture_consume") === "on",
      },
    };
  };

  const guestLoginCheckbox = el.querySelector("#auth-guest-login-enabled");
  const guestEmailDomainInput = el.querySelector("#guest-email-domain");
  if (guestLoginCheckbox && guestEmailDomainInput) {
    guestLoginCheckbox.addEventListener("change", () => {
      guestEmailDomainInput.disabled = !guestLoginCheckbox.checked;
      if (guestLoginCheckbox.checked) {
        guestEmailDomainInput.focus();
      }
    });
  }

  el.querySelector("#settings-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const guestLogin = ev.target.querySelector("#auth-guest-login-enabled");
    const guestDomain = ev.target.querySelector("#guest-email-domain");
    if (guestLogin && guestLogin.checked && !guestDomain?.value?.trim()) {
      guestDomain?.focus();
      showErrorToast({ message: "Guest Email Domain is required when guest email login is enabled" });
      return;
    }
    const payload = collectStructuredSettings(ev.target);
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings: payload });
    showOk("Settings saved");
    await renderAppSettings(el, appID);
  });

  el.querySelector("#save-raw-settings")?.addEventListener("click", async () => {
    const rawVal = el.querySelector("#app-settings-json")?.value || "{}";
    let parsed;
    try {
      parsed = JSON.parse(rawVal);
    } catch {
      throw new Error("Raw settings must be valid JSON");
    }
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings: parsed });
    showOk("Raw settings saved");
    await renderAppSettings(el, appID);
  });
}
