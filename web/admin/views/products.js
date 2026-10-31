import { api, esc, showOk } from "../api.js";
import { wireMobileSelect } from "../choice-sheet.js";

/** Currencies that Stripe treats as zero-decimal (price_amount == major units). */
const ZERO_DECIMAL = new Set([
  "bif", "clp", "djf", "gnf", "jpy", "kmf", "krw", "mga", "pyg", "rwf", "ugx", "vnd", "vuv", "xaf", "xof", "xpf",
]);

function currencyFactor(currency) {
  return ZERO_DECIMAL.has(String(currency || "").toLowerCase()) ? 1 : 100;
}

/** API stores integer minor units; form edits major units (e.g. 0.99 USD). */
function majorFromMinor(minor, currency) {
  const n = Number(minor);
  if (!Number.isFinite(n)) return "";
  const f = currencyFactor(currency);
  if (f === 1) return String(Math.round(n));
  return (n / f).toFixed(2);
}

function minorFromMajor(major, currency) {
  const n = Number(major);
  if (!Number.isFinite(n) || n < 0) return NaN;
  const f = currencyFactor(currency);
  return Math.round(n * f);
}

const CURRENCIES = [
  "usd", "eur", "gbp", "cad", "aud", "nzd", "chf", "sek", "nok", "dkk",
  "jpy", "cny", "hkd", "sgd", "krw", "inr", "brl", "mxn",
];

function currencyOptions(selected) {
  const cur = String(selected || "usd").toLowerCase();
  const list = CURRENCIES.includes(cur) ? CURRENCIES : [cur, ...CURRENCIES];
  return list.map((c) =>
    `<option value="${esc(c)}"${c === cur ? " selected" : ""}>${esc(c.toUpperCase())}</option>`
  ).join("");
}

/** Keep at most two fractional digits (zero for zero-decimal currencies). */
function clampPriceMajor(value, currency) {
  let s = String(value ?? "").trim();
  if (!s) return "";
  s = s.replace(/[^\d.]/g, "");
  const parts = s.split(".");
  if (parts.length > 2) s = parts[0] + "." + parts.slice(1).join("");
  const [whole, frac = ""] = s.split(".");
  if (currencyFactor(currency) === 1) return whole.replace(/^0+(?=\d)/, "") || (whole === "" ? "" : "0");
  if (frac.length <= 2) return frac.length ? `${whole || "0"}.${frac}` : (s.endsWith(".") ? `${whole || "0"}.` : whole);
  return `${whole || "0"}.${frac.slice(0, 2)}`;
}

function syncPriceStep(priceInput, currency) {
  if (!priceInput) return;
  const zero = currencyFactor(currency) === 1;
  priceInput.step = zero ? "1" : "0.01";
  priceInput.placeholder = zero ? "100" : "0.99";
  // Do not rewrite the amount on currency change — truncating 9.99→9 for JPY
  // would silently undercharge. Leave the value and let submit validation complain.
}

function priceFractionInvalid(rawMajor, currency) {
  const raw = String(rawMajor || "").trim();
  if (!raw) return false;
  const frac = raw.includes(".") ? raw.split(".")[1] || "" : "";
  const maxFrac = currencyFactor(currency) === 1 ? 0 : 2;
  return frac.length > maxFrac;
}

function productBody(fd, { includeActive }) {
  const currency = String(fd.get("currency") || "usd").trim().toLowerCase();
  const body = {
    sku: fd.get("sku"),
    name: fd.get("name"),
    description: fd.get("description") || "",
    credits: Number(fd.get("credits")),
    price_amount: minorFromMajor(fd.get("price_major"), currency),
    currency,
  };
  if (includeActive) body.active = fd.get("active") === "true";
  else body.active = true;
  const sortRaw = String(fd.get("sort_order") ?? "").trim();
  if (sortRaw !== "") {
    const sortNum = Number(sortRaw);
    if (Number.isFinite(sortNum)) body.sort_order = Math.trunc(sortNum);
  }
  for (const k of ["stripe_price_id", "apple_product_id", "google_product_id"]) {
    const v = String(fd.get(k) || "").trim();
    if (includeActive) body[k] = v || null;
    else if (v) body[k] = v;
  }
  return body;
}

function activeStamp(active) {
  return active
    ? `<span class="user-stamp user-stamp--active">active</span>`
    : `<span class="user-stamp user-stamp--disabled">inactive</span>`;
}

function priceLabel(minor, currency) {
  const cur = (currency || "").toUpperCase();
  const major = majorFromMinor(minor, currency);
  return major === "" ? "—" : `${major} ${cur}`.trim();
}

/** Set stagger delays via CSSOM — HTML style="" is blocked by admin CSP style-src. */
function applyRevealDelays(root, selector) {
  root.querySelectorAll(selector).forEach((node) => {
    const n = Number(node.getAttribute("data-reveal") || "0");
    if (Number.isFinite(n)) node.style.setProperty("--reveal", String(n));
  });
}

export async function renderProducts(el, appID) {
  el.innerHTML = `<div class="page-head"><h1>Products</h1><p class="muted">Loading…</p></div>`;
  const data = await api("GET", "/apps/" + encodeURIComponent(appID) + "/products");
  const items = data.items || [];
  const activeN = items.filter((p) => p.active).length;

  const rows = items.map((p, i) => `
    <tr class="prod-row" data-reveal="${i}">
      <td class="user-idx" aria-hidden="true">${String(i + 1).padStart(2, "0")}</td>
      <td>
        <div class="prod-identity">
          <span class="prod-identity__name">${esc(p.name)}</span>
          <span class="prod-identity__sku mono">${esc(p.sku)}</span>
        </div>
      </td>
      <td class="prod-credits">${esc(p.credits)}</td>
      <td class="prod-price">${esc(priceLabel(p.price_amount, p.currency))}</td>
      <td>${activeStamp(!!p.active)}</td>
      <td class="prod-actions">
        <button type="button" class="prod-edit edit-prod" data-id="${esc(p.id)}">Edit</button>
        <button type="button" class="prod-toggle toggle-prod" data-id="${esc(p.id)}" data-active="${p.active ? "1" : "0"}">
          ${p.active ? "Deactivate" : "Activate"}
        </button>
      </td>
    </tr>`).join("") || `<tr><td colspan="6" class="empty user-empty-row">No products yet — add one to sell credits.</td></tr>`;

  el.innerHTML = `
    <div class="page-head page-head-row user-page-head">
      <div>
        <p class="user-kicker">App · catalog</p>
        <h1 class="user-page-title">Products</h1>
        <p class="muted">Credit packs sold via Stripe Checkout or mobile IAP.</p>
      </div>
      <div class="prod-head-actions">
        <div class="user-count" aria-label="Active products">${activeN}<span class="ov-bound-total">/${items.length}</span></div>
        <button type="button" class="prod-create" id="add-prod">Add product</button>
      </div>
    </div>

    <div class="table-wrap apps-table-wrap prod-table-wrap">
      <table class="apps-table prod-table">
        <thead><tr>
          <th class="user-idx-h"></th>
          <th>Product</th><th>Credits</th><th>Price</th><th>Status</th><th></th>
        </tr></thead>
        <tbody>${rows}</tbody>
      </table>
    </div>

    <dialog id="prod-dialog" class="modal prod-modal">
      <div class="modal-card prod-modal__card">
        <div class="modal-head prod-modal__head">
          <div>
            <p class="user-kicker">Catalog</p>
            <h2 id="prod-dialog-title">Add product</h2>
          </div>
          <button type="button" class="secondary outline modal-close prod-modal__close" aria-label="Close">✕</button>
        </div>
        <form id="prod-form" class="prod-form">
          <input type="hidden" name="id" value="">
          <div class="form-row">
            <div class="field"><label>SKU</label><input name="sku" required autocomplete="off"></div>
            <div class="field"><label>Name</label><input name="name" required autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Description</label><input name="description" autocomplete="off"></div>
          </div>
          <div class="form-row form-row--price">
            <div class="field"><label>Credits</label><input name="credits" type="number" min="1" required></div>
            <div class="field field--price">
              <label>Price</label>
              <div class="prod-price-input">
                <select name="currency" id="prod-currency" class="prod-price-input__cur" required aria-label="Currency">
                  ${currencyOptions("usd")}
                </select>
                <input name="price_major" type="text" inputmode="decimal" required
                  class="prod-price-input__amt" placeholder="0.99" autocomplete="off" aria-label="Amount">
              </div>
            </div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Stripe price ID <span class="muted">(optional)</span></label><input name="stripe_price_id" placeholder="price_…" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Apple product ID <span class="muted">(optional)</span></label><input name="apple_product_id" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field field--full"><label>Google product ID <span class="muted">(optional)</span></label><input name="google_product_id" autocomplete="off"></div>
          </div>
          <div class="form-row">
            <div class="field"><label>Sort order <span class="muted">(optional)</span></label><input name="sort_order" type="number" step="1" value="0" autocomplete="off"></div>
          </div>
          <div class="form-row" id="prod-active-row" hidden>
            <div class="field field--status"><label>Active</label>
              <select name="active" id="prod-active">
                <option value="true">true</option>
                <option value="false">false</option>
              </select>
            </div>
          </div>
          <div class="form-actions">
            <button type="button" class="secondary modal-cancel">Cancel</button>
            <button type="submit" id="prod-submit">Create</button>
          </div>
        </form>
      </div>
    </dialog>`;

  applyRevealDelays(el, ".prod-row[data-reveal]");
  wireMobileSelect(el.querySelector("#prod-active"), { title: "Active" });
  wireMobileSelect(el.querySelector("#prod-currency"), {
    title: "Currency",
    inline: true,
    triggerClass: "choice-trigger choice-trigger--inline prod-price-input__cur-btn",
  });

  const byID = Object.fromEntries(items.map((p) => [p.id, p]));
  const dialog = el.querySelector("#prod-dialog");
  const form = el.querySelector("#prod-form");
  const titleEl = el.querySelector("#prod-dialog-title");
  const submitBtn = el.querySelector("#prod-submit");
  const activeRow = el.querySelector("#prod-active-row");
  const currencySel = form.querySelector("#prod-currency");
  const priceInput = form.querySelector('[name="price_major"]');

  function closeDialog() {
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
  }

  function openDialog(mode, product) {
    // Prefer elements.namedItem — form.id / form.name are reserved HTMLFormElement IDL attrs.
    const f = (n) => form.elements.namedItem(n);
    form.reset();
    f("id").value = "";
    f("currency").value = "usd";
    f("currency").dispatchEvent(new Event("change", { bubbles: true }));
    syncPriceStep(priceInput, "usd");
    if (mode === "edit" && product) {
      titleEl.textContent = "Edit product";
      submitBtn.textContent = "Save";
      activeRow.hidden = false;
      f("id").value = product.id;
      f("sku").value = product.sku;
      f("name").value = product.name;
      f("credits").value = product.credits;
      const cur = String(product.currency || "usd").toLowerCase();
      if (![...currencySel.options].some((o) => o.value === cur)) {
        currencySel.insertAdjacentHTML("afterbegin", `<option value="${esc(cur)}">${esc(cur.toUpperCase())}</option>`);
      }
      f("currency").value = cur;
      f("currency").dispatchEvent(new Event("change", { bubbles: true }));
      f("price_major").value = majorFromMinor(product.price_amount, cur);
      syncPriceStep(priceInput, cur);
      f("description").value = product.description || "";
      f("stripe_price_id").value = product.stripe_price_id || "";
      f("apple_product_id").value = product.apple_product_id || "";
      f("google_product_id").value = product.google_product_id || "";
      f("sort_order").value = product.sort_order != null ? String(product.sort_order) : "0";
      f("active").value = product.active ? "true" : "false";
      f("active").dispatchEvent(new Event("change", { bubbles: true }));
    } else {
      titleEl.textContent = "Add product";
      submitBtn.textContent = "Create";
      f("sort_order").value = "0";
      activeRow.hidden = true;
      f("price_major").value = "";
    }
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    f("sku").focus();
  }

  priceInput.addEventListener("input", () => {
    const cur = currencySel.value || "usd";
    const next = clampPriceMajor(priceInput.value, cur);
    if (next !== priceInput.value) {
      const start = priceInput.selectionStart;
      priceInput.value = next;
      if (typeof start === "number") {
        const pos = Math.min(start, next.length);
        priceInput.setSelectionRange(pos, pos);
      }
    }
    priceInput.setCustomValidity("");
  });

  currencySel.addEventListener("change", () => {
    const cur = currencySel.value || "usd";
    syncPriceStep(priceInput, cur);
    if (priceFractionInvalid(priceInput.value, cur)) {
      priceInput.setCustomValidity(
        currencyFactor(cur) === 1 ? "Enter a whole number" : "Enter a valid price with up to 2 decimals",
      );
    } else {
      priceInput.setCustomValidity("");
    }
  });

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
    const body = productBody(fd, { includeActive: !!id });
    const rawMajor = String(fd.get("price_major") || "").trim();
    if (!Number.isFinite(body.price_amount) || priceFractionInvalid(rawMajor, body.currency)) {
      priceInput.setCustomValidity(
        currencyFactor(body.currency) === 1 ? "Enter a whole number" : "Enter a valid price with up to 2 decimals",
      );
      priceInput.reportValidity();
      return;
    }
    priceInput.setCustomValidity("");
    if (id) {
      await api("PATCH", "/apps/" + encodeURIComponent(appID) + "/products/" + encodeURIComponent(id), body);
      showOk("Product updated");
    } else {
      await api("POST", "/apps/" + encodeURIComponent(appID) + "/products", body);
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
