import { api, esc, showOk } from "../api.js";

function productBody(fd, { includeActive }) {
  const body = {
    sku: fd.get("sku"),
    name: fd.get("name"),
    description: fd.get("description") || "",
    credits: Number(fd.get("credits")),
    price_amount: Number(fd.get("price_amount")),
    currency: fd.get("currency"),
  };
  if (includeActive) body.active = fd.get("active") === "true";
  else body.active = true;
  for (const k of ["stripe_price_id", "apple_product_id", "google_product_id"]) {
    const v = String(fd.get(k) || "").trim();
    if (includeActive) body[k] = v || null;
    else if (v) body[k] = v;
  }
  return body;
}

export async function renderProducts(el, appID) {
  el.innerHTML = `<h1>Products</h1><p class="muted">Loading…</p>`;
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/products");
  const items = data.items || [];
  const rows = items.map((p) => `
    <tr>
      <td class="mono">${esc(p.sku)}</td>
      <td>${esc(p.name)}</td>
      <td>${esc(p.credits)}</td>
      <td>${esc(p.price_amount)} ${esc(p.currency)}</td>
      <td>${p.active ? "yes" : "no"}</td>
      <td class="actions">
        <button type="button" class="secondary edit-prod" data-id="${esc(p.id)}">Edit</button>
        <button type="button" class="secondary toggle-prod" data-id="${esc(p.id)}" data-active="${p.active ? "1" : "0"}">
          ${p.active ? "Deactivate" : "Activate"}
        </button>
      </td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty">No products yet — add one to sell credits.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row">
      <div>
        <h1>Products</h1>
        <p class="muted">Credit packs sold via Stripe Checkout or mobile IAP.</p>
      </div>
      <button type="button" id="add-prod">Add product</button>
    </div>
    <div class="table-wrap">
    <table>
      <thead><tr><th>SKU</th><th>Name</th><th>Credits</th><th>Price</th><th>Active</th><th></th></tr></thead>
      <tbody>${rows}</tbody>
    </table>
    </div>
    <dialog id="prod-dialog" class="modal">
      <div class="modal-card">
        <div class="modal-head">
          <h2 id="prod-dialog-title">Add product</h2>
          <button type="button" class="secondary outline modal-close" aria-label="Close">✕</button>
        </div>
        <form id="prod-form">
          <input type="hidden" name="id" value="">
          <div class="form-row">
            <div class="field"><label>SKU</label><input name="sku" required autocomplete="off"></div>
            <div class="field"><label>Name</label><input name="name" required></div>
          </div>
          <div class="form-row">
            <div class="field"><label>Credits</label><input name="credits" type="number" min="1" required></div>
            <div class="field"><label>Price amount (minor units)</label><input name="price_amount" type="number" min="0" required></div>
            <div class="field"><label>Currency</label><input name="currency" value="usd" required></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Description</label><input name="description"></div>
          </div>
          <div class="form-row">
            <div class="field"><label>Stripe price ID</label><input name="stripe_price_id" placeholder="price_…"></div>
            <div class="field"><label>Apple product ID</label><input name="apple_product_id"></div>
            <div class="field"><label>Google product ID</label><input name="google_product_id"></div>
          </div>
          <div class="form-row" id="prod-active-row" hidden>
            <div class="field"><label>Active</label>
              <select name="active"><option value="true">true</option><option value="false">false</option></select>
            </div>
          </div>
          <div class="form-actions">
            <button type="button" class="secondary modal-cancel">Cancel</button>
            <button type="submit" id="prod-submit">Create</button>
          </div>
        </form>
      </div>
    </dialog>`;

  const byID = Object.fromEntries(items.map((p) => [p.id, p]));
  const dialog = el.querySelector("#prod-dialog");
  const form = el.querySelector("#prod-form");
  const titleEl = el.querySelector("#prod-dialog-title");
  const submitBtn = el.querySelector("#prod-submit");
  const activeRow = el.querySelector("#prod-active-row");

  function closeDialog() {
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }

  function openDialog(mode, product) {
    form.reset();
    form.id.value = "";
    form.currency.value = "usd";
    if (mode === "edit" && product) {
      titleEl.textContent = "Edit product";
      submitBtn.textContent = "Save";
      activeRow.hidden = false;
      form.id.value = product.id;
      form.sku.value = product.sku;
      form.name.value = product.name;
      form.credits.value = product.credits;
      form.price_amount.value = product.price_amount;
      form.currency.value = product.currency;
      form.description.value = product.description || "";
      form.stripe_price_id.value = product.stripe_price_id || "";
      form.apple_product_id.value = product.apple_product_id || "";
      form.google_product_id.value = product.google_product_id || "";
      form.active.value = product.active ? "true" : "false";
    } else {
      titleEl.textContent = "Add product";
      submitBtn.textContent = "Create";
      activeRow.hidden = true;
    }
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    form.sku.focus();
  }

  el.querySelector("#add-prod").addEventListener("click", () => openDialog("create"));
  el.querySelectorAll(".modal-close, .modal-cancel").forEach((btn) => {
    btn.addEventListener("click", closeDialog);
  });
  dialog.addEventListener("click", (ev) => {
    if (ev.target === dialog) closeDialog();
  });

  el.querySelectorAll(".edit-prod").forEach((btn) => {
    btn.addEventListener("click", () => {
      const p = byID[btn.dataset.id];
      if (p) openDialog("edit", p);
    });
  });

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const fd = new FormData(form);
    const id = String(fd.get("id") || "");
    if (id) {
      await api("PATCH", "/apps/" + encodeURIComponent(appID) + "/products/" + encodeURIComponent(id),
        productBody(fd, { includeActive: true }));
      showOk("Product updated");
    } else {
      await api("POST", "/apps/" + encodeURIComponent(appID) + "/products",
        productBody(fd, { includeActive: false }));
      showOk("Product created");
    }
    closeDialog();
    await renderProducts(el, appID);
  });

  el.querySelectorAll(".toggle-prod").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const active = btn.dataset.active !== "1";
      await api("PATCH", "/apps/" + encodeURIComponent(appID) + "/products/" + encodeURIComponent(btn.dataset.id), { active });
      showOk(active ? "Activated" : "Deactivated");
      await renderProducts(el, appID);
    });
  });
}
