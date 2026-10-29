import { api, esc, fmtTime, showOk } from "../api.js";

function scopesLabel(scopes) {
  if (!Array.isArray(scopes) || scopes.length === 0) return "default";
  return scopes.join(", ");
}

function keyStatusStamp(revokedAt) {
  return revokedAt
    ? `<span class="user-stamp user-stamp--disabled">revoked</span>`
    : `<span class="user-stamp user-stamp--active">active</span>`;
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

export async function renderKeys(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>API keys</h1><p class="muted">Loading…</p></div>`;
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/api-keys");
  const items = data.items || [];
  const activeN = items.filter((k) => !k.revoked_at).length;

  const rows = items.map((k, i) => `
    <tr class="keys-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <div class="keys-identity">
          <span class="keys-identity__name">${esc(k.name)}</span>
          <span class="keys-identity__prefix mono" title="${esc(k.prefix)}">${esc(k.prefix)}</span>
        </div>
      </td>
      <td class="keys-scopes" title="${esc(scopesLabel(k.scopes))}">${esc(scopesLabel(k.scopes))}</td>
      <td>${keyStatusStamp(k.revoked_at)}</td>
      <td class="user-when">${esc(fmtTime(k.last_used_at))}</td>
      <td class="user-when">${esc(fmtTime(k.created_at))}</td>
      <td class="keys-actions">
        ${k.revoked_at
          ? `<span class="user-empty">—</span>`
          : `<button type="button" class="keys-revoke revoke-key" data-id="${esc(k.id)}">Revoke</button>`}
      </td>
    </tr>`).join("") || `<tr><td colspan="7" class="empty user-empty-row">No API keys yet — create one to call the API.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · credentials</p>
        <h1 class="user-page-title">API keys</h1>
        <p class="muted">Secrets are shown once at creation — store them securely.</p>
      </div>
      <div class="keys-head-actions">
        <div class="user-count" aria-label="Active API keys">${activeN}<span class="ov-bound-total">/${items.length}</span></div>
        <button type="button" class="keys-create" id="add-key">Create key</button>
      </div>
    </div>

    <div class="table-wrap apps-table-wrap keys-table-wrap">
      <table class="apps-table keys-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>Key</th><th>Scopes</th><th>Status</th><th>Last used</th><th>Created</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>

    <dialog id="key-dialog" class="modal keys-modal">
      <div class="modal-card keys-modal__card">
        <div class="modal-head keys-modal__head">
          <div>
            <p class="user-kicker">API key</p>
            <h2 id="key-dialog-title">Create key</h2>
          </div>
          <button type="button" class="secondary outline modal-close keys-modal__close" aria-label="Close">✕</button>
        </div>
        <div id="key-form-panel">
          <form id="create-key" class="keys-create-form">
            <div class="form-row">
              <div class="field field--full"><label>Name</label><input name="name" required autocomplete="off" placeholder="e.g. production"></div>
            </div>
            <div class="form-row">
              <div class="field field--full">
                <label>Scopes (comma-separated, optional)</label>
                <input name="scopes" placeholder="leave empty for default" autocomplete="off">
              </div>
            </div>
            <div class="form-actions">
              <button type="button" class="secondary modal-cancel">Cancel</button>
              <button type="submit">Create</button>
            </div>
          </form>
        </div>
        <div id="key-reveal-panel" hidden>
          <div class="secret-once secret-once-strong keys-secret" role="alert">
            <p class="secret-once-warn"><strong>Copy this key now.</strong> It is shown only once and cannot be recovered.</p>
            <code id="key-secret-value" class="secret-once-value"></code>
          </div>
          <div class="form-actions">
            <button type="button" class="secondary" id="key-copy">Copy to clipboard</button>
            <button type="button" id="key-done">Done — I’ve copied it</button>
          </div>
        </div>
      </div>
    </dialog>`;

  applyRevealDelays(el, ".keys-row[data-reveal]");

  const dialog = el.querySelector("#key-dialog");
  const formPanel = el.querySelector("#key-form-panel");
  const revealPanel = el.querySelector("#key-reveal-panel");
  const titleEl = el.querySelector("#key-dialog-title");
  const form = el.querySelector("#create-key");
  const secretEl = el.querySelector("#key-secret-value");
  let revealing = false;
  let pendingSecret = "";

  function openDialog() {
    revealing = false;
    pendingSecret = "";
    form.reset();
    formPanel.hidden = false;
    revealPanel.hidden = true;
    titleEl.textContent = "Create key";
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    form.elements.namedItem("name").focus();
  }

  function showReveal(secret) {
    revealing = true;
    pendingSecret = secret;
    formPanel.hidden = true;
    revealPanel.hidden = false;
    titleEl.textContent = "Copy your API key";
    secretEl.textContent = secret;
  }

  async function finishAndRefresh() {
    revealing = false;
    pendingSecret = "";
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
    await renderKeys(el, appID);
  }

  function tryClose() {
    if (revealing) {
      if (!confirm("Have you copied the key? It will not be shown again.")) return;
      finishAndRefresh();
      return;
    }
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }

  el.querySelector("#add-key").addEventListener("click", openDialog);
  el.querySelectorAll(".modal-close, .modal-cancel").forEach((btn) => {
    btn.addEventListener("click", tryClose);
  });
  dialog.addEventListener("click", (ev) => {
    if (ev.target === dialog) tryClose();
  });
  dialog.addEventListener("cancel", (ev) => {
    if (!revealing) return;
    ev.preventDefault();
    tryClose();
  });

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    const scopesRaw = String(fd.get("scopes") || "").trim();
    const body = { name: fd.get("name") };
    if (scopesRaw) body.scopes = scopesRaw.split(",").map((s) => s.trim()).filter(Boolean);
    const created = await api("POST", "/apps/" + encodeURIComponent(appID) + "/api-keys", body);
    showOk("API key created");
    showReveal(created.key);
  });

  el.querySelector("#key-copy").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(pendingSecret);
      showOk("Copied to clipboard");
    } catch {
      secretEl.focus();
      const range = document.createRange();
      range.selectNodeContents(secretEl);
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      showOk("Select the key and copy manually (⌘/Ctrl+C)");
    }
  });

  el.querySelector("#key-done").addEventListener("click", () => finishAndRefresh());

  el.querySelectorAll(".revoke-key").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm("Revoke this API key?")) return;
      await api("DELETE", "/apps/" + encodeURIComponent(appID) + "/api-keys/" + encodeURIComponent(btn.dataset.id));
      showOk("Key revoked");
      await renderKeys(el, appID);
    });
  });
}
