import { api, esc, fmtTime, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

function extractVariables(text) {
  if (!text) return [];
  const matches = text.matchAll(/\{\{\{\s*([A-Za-z0-9_-]+)\s*\}\}\}/g);
  const seen = new Set();
  const res = [];
  for (const m of matches) {
    const v = m[1].trim();
    if (!seen.has(v)) {
      seen.add(v);
      res.push(v);
    }
  }
  return res;
}

export async function renderAI(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>AI Gateway</h1><p class="muted">Loading…</p></div>`;
  const [app, cfg, presetsData, connectionsData] = await Promise.all([
    api("GET", "/apps/" + encodeURIComponent(appID)),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/config"),
    api("GET", "/apps/" + encodeURIComponent(appID) + "/ai-presets"),
    api("GET", "/connections?provider=openai_compatible"),
  ]);
  const settings = cfg.settings || {};
  const aiSettings = settings.ai || {};
  const pricing = aiSettings.pricing || {};
  const presets = presetsData.items || [];
  const publishedN = presets.filter((p) => p.published).length;
  const boundInteg = (cfg.integrations && cfg.integrations.openai_compatible) || {};
  const isBound = !!(boundInteg.connection_id || boundInteg.source === "default");
  const boundLabel = boundInteg.connection_id === "default" || boundInteg.source === "default"
    ? "Env default"
    : (boundInteg.connection_id ? boundInteg.connection_id.slice(0, 8) : "None");

  const presetRows = presets.map((p, i) => {
    const vars = extractVariables(p.system_prompt + " " + p.user_prompt_template);
    const varsPills = vars.length
      ? vars.map((v) => `<code class="ai-pill mono">{{{${esc(v)}}}}</code>`).join(" ")
      : `<span class="user-empty">—</span>`;
    return `
    <tr class="prod-row" data-reveal="${i}" data-id="${esc(p.id)}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <div class="prod-identity">
          <span class="prod-identity__name">${esc(p.name)}</span>
          <span class="prod-identity__sku mono">${esc(p.slug)}</span>
        </div>
      </td>
      <td class="mono ai-model-tag">${esc(p.model)}</td>
      <td class="ai-vars-cell">${varsPills}</td>
      <td class="act-num"><strong>${esc(p.credits_cost)}</strong> <span class="muted">cr</span></td>
      <td>${p.published ? `<span class="user-stamp user-stamp--active">published</span>` : `<span class="user-stamp user-stamp--pending">draft</span>`}</td>
      <td class="prod-actions">
        <button type="button" class="prod-edit preset-run" data-slug="${esc(p.slug)}" title="Test run">Run</button>
        <button type="button" class="prod-edit preset-edit" data-id="${esc(p.id)}">Edit</button>
        <button type="button" class="keys-revoke preset-del" data-id="${esc(p.id)}">Delete</button>
      </td>
    </tr>`;
  }).join("") || `<tr><td colspan="7" class="empty user-empty-row">No presets yet — add one to configure server-side prompt templates.</td></tr>`;

  el.innerHTML = `
    <div class="page-head user-page-head page-head-row">
      <div>
        <p class="user-kicker">App · artificial intelligence</p>
        <h1 class="user-page-title">AI Gateway</h1>
        <p class="muted">Server-side OpenAI-compatible proxy with credit metering, SSE streaming, and hidden prompts.</p>
      </div>
      <div class="user-count" aria-label="Presets total">${presets.length}</div>
    </div>

    <article class="apps-create ai-policy-card">
      <div class="apps-create__head">
        <h2>Policy &amp; Limits</h2>
        <span class="apps-create__badge">policy</span>
      </div>
      <p class="muted email-card__sub">Protect backend API keys, restrict general chat, and limit streaming concurrency.</p>
      <form id="ai-settings-form" class="prod-form ai-policy-form">
        <div class="form-row ai-policy-row">
          <div class="field">
            <label class="ai-label-inline">Gateway</label>
            <select name="enabled" id="ai-enabled">
              <option value="true"${aiSettings.enabled !== false ? " selected" : ""}>Enabled</option>
              <option value="false"${aiSettings.enabled === false ? " selected" : ""}>Disabled</option>
            </select>
          </div>
          <div class="field">
            <label class="ai-label-inline">Direct Chat</label>
            <select name="allow_direct_chat" id="ai-direct-chat">
              <option value="true"${aiSettings.allow_direct_chat !== false ? " selected" : ""}>Allowed</option>
              <option value="false"${aiSettings.allow_direct_chat === false ? " selected" : ""}>Presets only</option>
            </select>
          </div>
          <div class="field">
            <label class="ai-label-inline">Max Concurrency</label>
            <input name="max_concurrent_per_user" type="number" min="1" max="10" value="${esc(aiSettings.max_concurrent_per_user || 2)}" placeholder="streams/user">
          </div>
          <div class="field">
            <label class="ai-label-inline">Default Cost (cr)</label>
            <input name="default_credits_per_request" type="number" min="0" value="${esc(pricing.default_credits_per_request != null ? pricing.default_credits_per_request : 1)}" placeholder="credits/req">
          </div>
        </div>
        <div class="form-actions">
          <button type="submit">Save policy</button>
        </div>
      </form>
    </article>

    <section class="email-block">
      <div class="email-block__head">
        <p class="user-kicker">Presets</p>
        <div class="email-templates__bar">
          <h2>Prompt Presets</h2>
          <div class="prod-head-actions">
            <div class="user-count" aria-label="Published presets">${publishedN}<span class="ov-bound-total">/${presets.length}</span></div>
            <button type="button" class="prod-create" id="preset-add">Add preset</button>
          </div>
        </div>
        <p class="muted">System Prompts stay server-side. Clients invoke <code>/presets/{slug}/run</code> with variables only.</p>
      </div>
      <div class="table-wrap apps-table-wrap prod-table-wrap">
        <table class="apps-table prod-table">
          <thead><tr>
            <th class="user-idx-h"></th>
            <th>Preset</th>
            <th>Model</th>
            <th>Variables</th>
            <th>Cost</th>
            <th>Status</th>
            <th></th>
          </tr></thead>
          <tbody>${presetRows}</tbody>
        </table>
      </div>
    </section>

    <dialog id="preset-dialog" class="modal prod-modal ai-preset-modal">
      <div class="modal-card prod-modal__card">
        <div class="modal-head prod-modal__head">
          <div>
            <p class="user-kicker">AI Preset</p>
            <h2 id="preset-dialog-title">Add preset</h2>
          </div>
          <button type="button" class="secondary outline modal-close prod-modal__close" aria-label="Close">✕</button>
        </div>
        <form id="preset-form" class="prod-form">
          <input type="hidden" name="id">
          <div class="form-row">
            <div class="field"><label>Name</label><input name="name" required placeholder="Translation Assistant" autocomplete="off"></div>
            <div class="field"><label>Slug</label><input name="slug" required pattern="[a-z0-9_-]{1,64}" placeholder="translate-text" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field"><label>Model</label><input name="model" required placeholder="gpt-4o-mini" autocomplete="off"></div>
            <div class="field"><label>Credit cost</label><input name="credits_cost" type="number" min="0" value="1" required></div>
          </div>
          <div class="form-row">
            <div class="field field--full">
              <label>System prompt</label>
              <textarea name="system_prompt" rows="5" placeholder="You are a professional translator. Target: {{{TARGET}}}."></textarea>
              <p class="hint">Use <code>{{{VAR}}}</code> for dynamic variables supplied by client requests.</p>
            </div>
          </div>
          <div class="form-row">
            <div class="field field--full">
              <label>User prompt template (optional)</label>
              <textarea name="user_prompt_template" rows="3" placeholder="Translate this text: {{{TEXT}}}"></textarea>
              <p class="hint">If omitted, defaults to <code>{{{INPUT}}}</code> variable from client.</p>
            </div>
          </div>
          <div class="form-row">
            <div class="field"><label>Temperature</label><input name="temperature" type="number" step="0.1" min="0" max="2" value="0.7"></div>
            <div class="field"><label>Max tokens</label><input name="max_tokens" type="number" min="1" max="32768" value="2048"></div>
          </div>
          <div class="form-row">
            <div class="field field--status">
              <label>Status</label>
              <select name="published" id="preset-published">
                <option value="true">published</option>
                <option value="false">draft</option>
              </select>
            </div>
          </div>
          <div class="form-actions">
            <button type="button" class="secondary modal-cancel">Cancel</button>
            <button type="submit" id="preset-submit">Save preset</button>
          </div>
        </form>
      </div>
    </dialog>

    <dialog id="run-dialog" class="modal prod-modal ai-preset-modal">
      <div class="modal-card prod-modal__card">
        <div class="modal-head prod-modal__head">
          <div>
            <p class="user-kicker">Interactive Inspector</p>
            <h2 id="run-dialog-title">Test Preset</h2>
          </div>
          <button type="button" class="secondary outline modal-close prod-modal__close" id="run-close" aria-label="Close">✕</button>
        </div>
        <div class="ai-runner-wrap">
          <form id="run-form" class="prod-form">
            <div id="run-vars-fields"></div>
            <div class="form-actions">
              <button type="submit" id="run-btn">Run stream</button>
            </div>
          </form>
          <div class="ai-run-output-block">
            <label class="muted">Live Response</label>
            <pre id="run-output" class="ai-terminal mono">Waiting for input…</pre>
          </div>
        </div>
      </div>
    </dialog>`;

  applyRevealDelays(el, ".prod-row[data-reveal]");
  wireMobileSelect(el.querySelector("#ai-enabled"), { title: "AI Enabled" });
  wireMobileSelect(el.querySelector("#ai-direct-chat"), { title: "Direct Chat" });
  wireMobileSelect(el.querySelector("#preset-published"), { title: "Status" });

  const byID = Object.fromEntries(presets.map((p) => [p.id, p]));
  const bySlug = Object.fromEntries(presets.map((p) => [p.slug, p]));
  const dialog = el.querySelector("#preset-dialog");
  const form = el.querySelector("#preset-form");
  const runDialog = el.querySelector("#run-dialog");
  const runForm = el.querySelector("#run-form");
  const runVarsContainer = el.querySelector("#run-vars-fields");
  const runOutput = el.querySelector("#run-output");
  let activeRunSlug = "";

  function closeDialog() {
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }

  function openDialog(preset) {
    form.reset();
    form.elements.namedItem("id").value = preset ? preset.id : "";
    el.querySelector("#preset-dialog-title").textContent = preset ? "Edit preset" : "Add preset";
    el.querySelector("#preset-submit").textContent = preset ? "Save" : "Create";
    if (preset) {
      form.elements.namedItem("name").value = preset.name || "";
      form.elements.namedItem("slug").value = preset.slug || "";
      form.elements.namedItem("model").value = preset.model || "";
      form.elements.namedItem("credits_cost").value = preset.credits_cost != null ? preset.credits_cost : 1;
      form.elements.namedItem("system_prompt").value = preset.system_prompt || "";
      form.elements.namedItem("user_prompt_template").value = preset.user_prompt_template || "";
      form.elements.namedItem("temperature").value = preset.temperature != null ? preset.temperature : 0.7;
      form.elements.namedItem("max_tokens").value = preset.max_tokens != null ? preset.max_tokens : 2048;
      form.elements.namedItem("published").value = preset.published ? "true" : "false";
    }
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  function openRunTester(slug) {
    const p = bySlug[slug];
    if (!p) return;
    activeRunSlug = slug;
    el.querySelector("#run-dialog-title").textContent = "Test: " + p.name;
    const vars = extractVariables(p.system_prompt + " " + p.user_prompt_template);
    if (!vars.length) vars.push("INPUT");
    runVarsContainer.innerHTML = vars.map((v) => `
      <div class="field">
        <label>${esc(v)}</label>
        <input name="var_${esc(v)}" required placeholder="Value for ${esc(v)}" autocomplete="off">
      </div>
    `).join("");
    runOutput.textContent = "Click 'Run stream' to execute…";
    if (typeof runDialog.showModal === "function") runDialog.showModal();
    else runDialog.setAttribute("open", "");
  }

  el.querySelector("#ai-settings-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(ev.target);
    const updatedAI = {
      ...(aiSettings || {}),
      enabled: fd.get("enabled") === "true",
      allow_direct_chat: fd.get("allow_direct_chat") === "true",
      max_concurrent_per_user: Number(fd.get("max_concurrent_per_user")) || 2,
      pricing: {
        ...(pricing || {}),
        default_credits_per_request: Number(fd.get("default_credits_per_request")) || 1,
      },
    };
    const nextSettings = { ...(settings || {}), ai: updatedAI };
    await api("PUT", "/apps/" + encodeURIComponent(appID) + "/config", { settings: nextSettings });
    showOk("AI policy saved");
    await renderAI(el, appID);
  });

  el.querySelector("#preset-add").addEventListener("click", () => openDialog(null));
  el.querySelectorAll(".modal-close, .modal-cancel").forEach((btn) => {
    btn.addEventListener("click", closeDialog);
  });
  el.querySelector("#run-close").addEventListener("click", () => {
    if (typeof runDialog.close === "function") runDialog.close();
    else runDialog.removeAttribute("open");
  });

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    const body = {
      name: String(fd.get("name") || "").trim(),
      slug: String(fd.get("slug") || "").trim(),
      model: String(fd.get("model") || "").trim(),
      credits_cost: Number(fd.get("credits_cost")),
      system_prompt: String(fd.get("system_prompt") || ""),
      user_prompt_template: String(fd.get("user_prompt_template") || ""),
      temperature: Number(fd.get("temperature")),
      max_tokens: Number(fd.get("max_tokens")),
      published: fd.get("published") === "true",
    };
    const id = String(fd.get("id") || "");
    if (id) {
      await api("PATCH", "/apps/" + encodeURIComponent(appID) + "/ai-presets/" + encodeURIComponent(id), body);
      showOk("Preset updated");
    } else {
      await api("POST", "/apps/" + encodeURIComponent(appID) + "/ai-presets", body);
      showOk("Preset created");
    }
    closeDialog();
    await renderAI(el, appID);
  });

  el.querySelectorAll(".preset-edit").forEach((btn) => {
    btn.addEventListener("click", () => openDialog(byID[btn.dataset.id]));
  });

  el.querySelectorAll(".preset-run").forEach((btn) => {
    btn.addEventListener("click", () => openRunTester(btn.dataset.slug));
  });

  el.querySelectorAll(".preset-del").forEach((btn) => {
    btn.addEventListener("click", async () => {
      if (!confirm("Delete this AI preset?")) return;
      await api("DELETE", "/apps/" + encodeURIComponent(appID) + "/ai-presets/" + encodeURIComponent(btn.dataset.id));
      showOk("Deleted");
      await renderAI(el, appID);
    });
  });

  runForm.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(runForm);
    const vars = {};
    for (const [k, v] of fd.entries()) {
      if (k.startsWith("var_")) vars[k.slice(4)] = v;
    }
    runOutput.textContent = "Connecting to upstream…\n";
    const btn = el.querySelector("#run-btn");
    btn.disabled = true;
    try {
      const token = sessionStorage.getItem("builtly_admin_token") || "";
      const res = await fetch("/api/v1/ai/presets/" + encodeURIComponent(activeRunSlug) + "/run", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-App-ID": appID,
          "Authorization": "Bearer " + token,
        },
        body: JSON.stringify({ variables: vars, stream: true }),
      });
      if (!res.ok) {
        const err = await res.text();
        runOutput.textContent = `[Error ${res.status}]\n` + err;
        return;
      }
      runOutput.textContent = "";
      const reader = res.body.getReader();
      const decoder = new TextDecoder("utf-8");
      let buf = "";
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop() || "";
        for (const line of lines) {
          if (line.startsWith("data: ")) {
            const raw = line.slice(6).trim();
            if (raw === "[DONE]") {
              runOutput.textContent += "\n[Stream Completed]";
              continue;
            }
            try {
              const chunk = JSON.parse(raw);
              const delta = chunk.choices?.[0]?.delta?.content || "";
              if (delta) runOutput.textContent += delta;
            } catch {
              // Ignore unparseable lines
            }
          }
        }
      }
    } catch (e) {
      runOutput.textContent += "\n[Network Error]: " + e.message;
    } finally {
      btn.disabled = false;
    }
  });
}
