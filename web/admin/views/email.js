import { api, esc, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

function varRows(variables) {
  const rows = (variables && variables.length) ? variables : [];
  return rows.map((v) => variableRow(v)).join("") + (rows.length ? "" : "");
}

function variableRow(v = {}) {
  const type = v.type === "number" ? "number" : "string";
  let fallback = "";
  if (v.fallback != null) fallback = typeof v.fallback === "string" ? v.fallback : String(v.fallback);
  return `<div class="email-var">
    <div class="field"><label>Key</label><input name="var_key" value="${esc(v.key || "")}" placeholder="PRODUCT" autocomplete="off"></div>
    <div class="field"><label>Type</label>
      <select name="var_type" class="email-var-type">
        <option value="string"${type === "string" ? " selected" : ""}>string</option>
        <option value="number"${type === "number" ? " selected" : ""}>number</option>
      </select>
    </div>
    <div class="field"><label>Fallback</label><input name="var_fallback" value="${esc(fallback)}" placeholder="optional" autocomplete="off"></div>
    <button type="button" class="email-var-del">Remove</button>
  </div>`;
}

function sampleVars(form, appName, sender) {
  const vars = {
    APP_NAME: appName || "App",
    EMAIL: "you@example.com",
    SUPPORT_ADDRESS: sender.reply_to || "support@example.com",
    ACTION_URL: (sender.action_base_url || "https://example.com").replace(/\/$/, "") + "/verify?token=preview",
    EXPIRY: "in 24 hours",
  };
  for (const v of collectVariables(form)) {
    if (v.fallback != null && v.fallback !== "") vars[v.key] = String(v.fallback);
    else vars[v.key] = v.type === "number" ? "1" : "sample";
  }
  return vars;
}

function fillPlaceholders(source, vars) {
  return String(source || "").replace(/\{\{\{\s*([A-Z][A-Z0-9_]*)\s*\}\}\}/g, (_, key) => {
    const value = Object.prototype.hasOwnProperty.call(vars, key) ? vars[key] : key;
    return esc(value);
  });
}

function renderPreview(host, html) {
  const root = host.shadowRoot || host.attachShadow({ mode: "open" });
  root.replaceChildren();
  const body = document.createElement("div");
  body.innerHTML = String(html || "").replace(/<script\b[^>]*>[\s\S]*?<\/script>/gi, "");
  root.append(body);
}

function collectVariables(form) {
  const rows = [...form.querySelectorAll(".email-var")];
  const variables = [];
  for (const row of rows) {
    const key = String(row.querySelector('[name="var_key"]').value || "").trim();
    if (!key) continue;
    const type = row.querySelector('[name="var_type"]').value || "string";
    const raw = String(row.querySelector('[name="var_fallback"]').value || "");
    const item = { key, type };
    if (raw.trim() !== "") {
      item.fallback = type === "number" ? Number(raw) : raw;
    }
    variables.push(item);
  }
  return variables;
}

function templateOptions(templates, selected) {
  return (templates || []).filter((t) => t.published || t.id === selected).map((t) => {
    const sel = selected && selected === t.id ? " selected" : "";
    const draft = t.published ? "" : " · draft";
    return `<option value="${esc(t.id)}"${sel}>${esc(t.name)} (${esc(t.key)}${esc(draft)})</option>`;
  }).join("");
}

export async function renderEmail(el, appID) {
  const [app, cfg, templates, triggers] = await Promise.all([
    api("GET", "/apps/" + encodeURIComponent(appID)),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/config"),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/email-templates"),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/email-triggers"),
  ]);
  const settings = cfg.settings || {};
  const sender = settings.email || {};
  const items = templates.items || [];
  const trigs = triggers.items || [];

  const triggerRows = trigs.map((t, i) => {
    const blank = t.required ? "Built-in" : "Do not send";
    const policy = t.required
      ? "Always sent. You can only replace the copy."
      : "Off until you pick a template.";
    const options = `<option value="">${esc(blank)}</option>` + templateOptions(items, t.template_id);
    return `<tr class="prod-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <div class="prod-identity">
          <span class="prod-identity__name">${esc(t.label)}</span>
          <span class="prod-identity__sku mono">${esc(t.trigger)}</span>
        </div>
        <p class="hint">${esc(policy)}</p>
      </td>
      <td>
        <select class="email-trigger" data-trigger="${esc(t.trigger)}">${options}</select>
        <p class="hint">Injected: ${(t.injected || []).map((k) => `<code>{{{${esc(k)}}}}</code>`).join(" ")}</p>
      </td>
    </tr>`;
  }).join("");

  const publishedN = items.filter((t) => t.published).length;
  const rows = items.map((t, i) => `<tr class="prod-row" data-reveal="${i}" data-id="${esc(t.id)}">
    <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
    <td>
      <div class="prod-identity">
        <span class="prod-identity__name">${esc(t.name)}</span>
        <span class="prod-identity__sku mono">${esc(t.key)}</span>
      </div>
    </td>
    <td>${esc(t.subject)}</td>
    <td>${t.published ? `<span class="user-stamp user-stamp--active">published</span>` : `<span class="user-stamp user-stamp--pending">draft</span>`}</td>
    <td class="prod-actions">
      <button type="button" class="prod-edit email-edit" data-id="${esc(t.id)}">Edit</button>
      <button type="button" class="keys-revoke email-del" data-id="${esc(t.id)}">Delete</button>
    </td>
  </tr>`).join("") || `<tr><td colspan="5" class="empty user-empty-row">No templates yet — add one to override a trigger or send from the product API.</td></tr>`;

  el.innerHTML = `
    <div class="page-head user-page-head">
      <div>
        <p class="user-kicker">App · email</p>
        <h1 class="user-page-title">Email</h1>
        <p class="muted">Per-app templates. System triggers can override built-in copy. Custom templates are sent by the product API.</p>
      </div>
    </div>

    <section class="email-block">
      <div class="email-block__head">
        <p class="user-kicker">Identity</p>
        <h2>Sender</h2>
        <p class="muted">Not tied to a template. The domain must be verified on the Resend account bound to this app. Empty from address means mail is skipped.</p>
      </div>
      <form id="email-sender" class="prod-form">
        <div class="form-row">
          <div class="field"><label>From name</label><input name="from_name" maxlength="80" value="${esc(sender.from_name || "")}" placeholder="${esc(app.name || "Acme")}" autocomplete="off"></div>
          <div class="field"><label>From address</label><input name="from_address" type="email" maxlength="320" value="${esc(sender.from_address || "")}" placeholder="noreply@example.com" autocomplete="off"></div>
        </div>
        <div class="form-row">
          <div class="field"><label>Reply-to</label><input name="reply_to" type="email" maxlength="320" value="${esc(sender.reply_to || "")}" placeholder="support@example.com" autocomplete="off"></div>
          <div class="field"><label>Action base URL</label><input name="action_base_url" type="url" value="${esc(sender.action_base_url || "")}" placeholder="https://app.example.com" autocomplete="off"></div>
        </div>
        <p class="hint">Action base URL is where verify and reset links land, for example <code>{url}/verify?token=…</code>.</p>
        <div class="form-actions">
          <button type="submit">Save sender</button>
        </div>
      </form>
    </section>

    <section class="email-block">
      <div class="email-block__head">
        <p class="user-kicker">System</p>
        <h2>Triggers</h2>
        <p class="muted">Security mail always sends. Built-in means the embedded copy. Welcome stays off until a published template is selected.</p>
      </div>
      <div class="table-wrap apps-table-wrap prod-table-wrap">
        <table class="apps-table prod-table email-trigger-table">
          <thead><tr>
            <th class="user-idx-h"></th>
            <th>Trigger</th>
            <th>Template</th>
          </tr></thead>
          <tbody>${triggerRows}</tbody>
        </table>
      </div>
    </section>

    <section class="email-block">
      <div class="email-block__head">
        <p class="user-kicker">Library</p>
        <div class="email-templates__bar">
          <h2>Templates</h2>
          <div class="prod-head-actions">
            <div class="user-count" aria-label="Published templates">${publishedN}<span class="ov-bound-total">/${items.length}</span></div>
            <button type="button" class="prod-create" id="email-add">Add template</button>
          </div>
        </div>
      </div>
      <div class="table-wrap apps-table-wrap prod-table-wrap">
        <table class="apps-table prod-table">
          <thead><tr>
            <th class="user-idx-h"></th>
            <th>Template</th>
            <th>Subject</th>
            <th>Status</th>
            <th></th>
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
    </section>

    <dialog id="email-dialog" class="modal prod-modal email-modal">
      <div class="modal-card prod-modal__card">
        <div class="modal-head prod-modal__head">
          <div>
            <p class="user-kicker">Template</p>
            <h2 id="email-dialog-title">Add template</h2>
          </div>
          <button type="button" class="secondary outline modal-close prod-modal__close" aria-label="Close">✕</button>
        </div>
        <form id="email-form" class="prod-form email-form">
          <input type="hidden" name="id">
          <div class="form-row">
            <div class="field"><label>Name</label><input name="name" required maxlength="80" placeholder="Order confirmation" autocomplete="off"></div>
            <div class="field"><label>Alias</label><input name="key" required maxlength="64" placeholder="order-confirmation" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Subject</label><input name="subject" required maxlength="200" placeholder="Thanks for {{{PRODUCT}}}" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>HTML</label><textarea name="html" rows="8" required placeholder="<p>Hello {{{EMAIL}}}</p>"></textarea></div>
          </div>
          <div class="field field--full">
            <label>Variables</label>
            <p class="hint">Up to 20. Keys are <code>UPPER_SNAKE</code>. <code>APP_NAME</code>, <code>EMAIL</code>, and <code>SUPPORT_ADDRESS</code> are filled by Builtly. Leave fallback blank to require a value at send time.</p>
            <div id="email-vars" class="email-vars"></div>
            <button type="button" class="secondary email-add-var" id="email-add-var">Add variable</button>
          </div>
          <div class="form-row">
            <div class="field field--status"><label>Status</label>
              <select name="published" id="email-published">
                <option value="false">draft</option>
                <option value="true">published</option>
              </select>
            </div>
          </div>
          <p class="hint">Only published templates can be bound or sent. Security templates must include <code>{{{ACTION_URL}}}</code>.</p>
          <div class="form-actions">
            <button type="button" class="secondary modal-cancel">Cancel</button>
            <button type="button" class="secondary" id="email-preview-btn">Preview</button>
            <button type="submit" id="email-submit">Create</button>
          </div>
        </form>
      </div>
    </dialog>

    <dialog id="email-preview" class="modal prod-modal email-modal">
      <div class="modal-card prod-modal__card">
        <div class="modal-head prod-modal__head">
          <div>
            <p class="user-kicker">Preview</p>
            <h2>Sample render</h2>
          </div>
          <button type="button" class="secondary outline modal-close prod-modal__close" id="email-preview-close" aria-label="Close">✕</button>
        </div>
        <p class="email-preview__meta" id="email-preview-meta"></p>
        <div id="email-preview-host" class="email-preview__host"></div>
      </div>
    </dialog>`;

  applyRevealDelays(el, ".prod-row[data-reveal]");
  wireMobileSelect(el.querySelector("#email-published"), { title: "Status" });
  const byID = Object.fromEntries(items.map((t) => [t.id, t]));
  const dialog = el.querySelector("#email-dialog");
  const form = el.querySelector("#email-form");
  const varsEl = el.querySelector("#email-vars");

  function closeDialog() {
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }
  function wireVarTypes() {
    varsEl.querySelectorAll(".email-var-type").forEach((sel) => wireMobileSelect(sel, { title: "Type" }));
  }
  function openDialog(template) {
    form.reset();
    form.elements.namedItem("id").value = template ? template.id : "";
    const status = form.elements.namedItem("published");
    status.value = template && template.published ? "true" : "false";
    status.dispatchEvent(new Event("change", { bubbles: true }));
    el.querySelector("#email-dialog-title").textContent = template ? "Edit template" : "Add template";
    el.querySelector("#email-submit").textContent = template ? "Save" : "Create";
    varsEl.innerHTML = template ? varRows(template.variables) : "";
    wireVarTypes();
    if (template) {
      form.elements.namedItem("name").value = template.name || "";
      form.elements.namedItem("key").value = template.key || "";
      form.elements.namedItem("subject").value = template.subject || "";
      form.elements.namedItem("html").value = template.html || "";
    }
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  function currentSender() {
    const form = el.querySelector("#email-sender");
    const fd = new FormData(form);
    return {
      from_name: String(fd.get("from_name") || "").trim(),
      from_address: String(fd.get("from_address") || "").trim(),
      reply_to: String(fd.get("reply_to") || "").trim(),
      action_base_url: String(fd.get("action_base_url") || "").trim(),
    };
  }

  function openPreview() {
    const vars = sampleVars(form, app.name, currentSender());
    const subject = fillPlaceholders(form.elements.namedItem("subject").value, vars);
    const from = currentSender();
    const fromLine = from.from_address
      ? (from.from_name ? from.from_name + " <" + from.from_address + ">" : from.from_address)
      : "From address not set";
    el.querySelector("#email-preview-meta").innerHTML =
      `<div><strong>From</strong> ${esc(fromLine)}</div><div><strong>Subject</strong> ${subject}</div>`;
    renderPreview(el.querySelector("#email-preview-host"), fillPlaceholders(form.elements.namedItem("html").value, vars));
    const preview = el.querySelector("#email-preview");
    if (typeof preview.showModal === "function") preview.showModal();
    else preview.setAttribute("open", "");
  }

  el.querySelector("#email-sender").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const next = {
      ...(settings || {}),
      email: { ...(settings.email || {}), ...currentSender() },
    };
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings: next });
    showOk("Sender saved");
  });

  el.querySelector("#email-add").addEventListener("click", () => openDialog(null));
  el.querySelector("#email-preview-btn").addEventListener("click", openPreview);
  el.querySelector("#email-preview-close").addEventListener("click", () => {
    const preview = el.querySelector("#email-preview");
    if (typeof preview.close === "function") preview.close();
    else preview.removeAttribute("open");
  });
  el.querySelectorAll(".modal-close, .modal-cancel").forEach((btn) => {
    btn.addEventListener("click", closeDialog);
  });
  el.querySelector("#email-add-var").addEventListener("click", () => {
    varsEl.insertAdjacentHTML("beforeend", variableRow());
    wireVarTypes();
  });
  varsEl.addEventListener("click", (ev) => {
    const btn = ev.target.closest(".email-var-del");
    if (btn) btn.closest(".email-var")?.remove();
  });

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    const body = {
      key: String(fd.get("key") || "").trim(),
      name: String(fd.get("name") || "").trim(),
      subject: String(fd.get("subject") || "").trim(),
      html: String(fd.get("html") || ""),
      variables: collectVariables(form),
      published: form.elements.namedItem("published").value === "true",
    };
    const id = String(fd.get("id") || "");
    if (id) {
      await api("PATCH", "/apps/" + encodeURIComponent(appID) + "/email-templates/" + encodeURIComponent(id), body);
      showOk("Template saved");
    } else {
      await api("POST", "/apps/" + encodeURIComponent(appID) + "/email-templates", body);
      showOk("Template created");
    }
    closeDialog();
    await renderEmail(el, appID);
  });

  el.querySelectorAll(".email-edit").forEach((btn) => {
    btn.addEventListener("click", () => openDialog(byID[btn.dataset.id]));
  });
  el.querySelectorAll(".email-del").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm("Delete this template?")) return;
      await api("DELETE", "/apps/" + encodeURIComponent(appID) + "/email-templates/" + encodeURIComponent(btn.dataset.id));
      showOk("Deleted");
      await renderEmail(el, appID);
    });
  });
  el.querySelectorAll(".email-trigger").forEach((sel) => {
    sel.dataset.prev = sel.value;
    sel.addEventListener("change", async () => {
      const templateID = sel.value || null;
      try {
        await api("PUT", "/apps/" + encodeURIComponent(appID) + "/email-triggers/" + encodeURIComponent(sel.dataset.trigger), {
          template_id: templateID,
        });
        sel.dataset.prev = sel.value;
        showOk("Trigger updated");
      } catch {
        sel.value = sel.dataset.prev || "";
      }
    });
  });
}
