import { api, clearToken, getToken, hydrate, onError, onOk, setToken, esc } from "./api.js";
import {
  clearAppsCache,
  getAppsCache,
  getCurrentAppID,
  hashForApp,
  sectionFromParts,
  setAppsCache,
  setCurrentAppID,
} from "./context.js";
import { closeChoiceSheet, openAppSwitcher, syncChromeLock } from "./choice-sheet.js";
import { showErrorToast, showOkToast } from "./toast.js";
import { renderAppsList, renderAppDetail, renderAppConfig } from "./views/apps.js";
import { renderConnections } from "./views/connections.js";
import { renderKeys } from "./views/keys.js";
import { renderProducts } from "./views/products.js";
import { renderUsers, renderUserDetail } from "./views/users.js";
import { renderReferrals } from "./views/referrals.js";
import { renderActivity } from "./views/activity.js";
import { renderOutbox } from "./views/outbox.js";
import { renderAudit } from "./views/audit.js";

const root = document.getElementById("app");

/** @type {HTMLElement|null} */
let mainEl = null;

onError((err) => {
  // clearError() calls onError(null) before each request — leave existing toasts.
  if (!err) return;
  showErrorToast(err);
});

onOk((msg) => {
  showOkToast(msg);
});

function parseRoute() {
  let raw = (location.hash || "#/apps").replace(/^#/, "");
  const q = raw.indexOf("?");
  if (q >= 0) raw = raw.slice(0, q);
  return raw.split("/").filter(Boolean);
}

async function loadApps() {
  const cached = getAppsCache();
  if (cached) return cached;
  const data = await api("GET", "/apps?limit=100");
  const items = data.items || [];
  setAppsCache(items);
  return items;
}

function navLink(href, label, active) {
  return `<a href="${href}"${active ? ' aria-current="page"' : ""}>${esc(label)}</a>`;
}

function shellHTML(parts, apps) {
  const inApp = parts[0] === "apps" && !!parts[1];
  const appID = inApp ? parts[1] : getCurrentAppID();
  const current = apps.find((a) => a.id === appID) || null;
  const section = sectionFromParts(parts);

  const switcherOpts = [
    `<option value="">Select an app…</option>`,
    ...apps.map((a) => {
      const sel = current && a.id === current.id ? " selected" : "";
      return `<option value="${esc(a.id)}"${sel}>${esc(a.name)} (${esc(a.slug)})</option>`;
    }),
  ].join("");

  const appNav = current
    ? `
      <div class="nav-section">App</div>
      ${navLink(hashForApp(current.id, "overview"), "Overview", inApp && section === "overview")}
      ${navLink(hashForApp(current.id, "config"), "Integrations", inApp && section === "config")}
      ${navLink(hashForApp(current.id, "keys"), "API keys", inApp && section === "keys")}
      ${navLink(hashForApp(current.id, "products"), "Products", inApp && section === "products")}
      ${navLink(hashForApp(current.id, "users"), "Users", inApp && section === "users")}
      ${navLink(hashForApp(current.id, "activity"), "Activity", inApp && section === "activity")}
      ${navLink(hashForApp(current.id, "referrals"), "Referrals", inApp && section === "referrals")}
    `
    : `
      <div class="nav-section">App</div>
      <p class="nav-hint">Create or select an app to configure auth, billing, and keys.</p>
    `;

  const appSlug = current ? current.slug : "";

  return `
    <header class="mobile-bar">
      <button type="button" class="mobile-bar__menu" id="sidebar-open"
        aria-label="Open menu" aria-controls="sidebar" aria-expanded="false">
        <span class="mobile-bar__burger" aria-hidden="true"></span>
      </button>
      <div class="mobile-bar__brand">
        Builtly <span class="mobile-bar__mark">admin</span>
      </div>
      ${appSlug
        ? `<button type="button" class="mobile-bar__app" id="mobile-app-chip" aria-haspopup="dialog" aria-controls="choice-sheet" aria-expanded="false">${esc(appSlug)}</button>`
        : `<span class="mobile-bar__app mobile-bar__app--empty"></span>`}
    </header>
    <div class="sidebar-backdrop" id="sidebar-backdrop" hidden></div>
    <aside class="sidebar" id="sidebar">
      <div class="sidebar__head">
        <div class="brand">
          <span class="brand__name">Builtly</span>
          <span class="brand__mark">admin</span>
        </div>
        <button type="button" class="sidebar__close" id="sidebar-close" aria-label="Close menu">✕</button>
      </div>
      <label class="app-switcher app-switcher--select">
        <span>Current app</span>
        <select id="app-switcher" ${apps.length ? "" : "disabled"}>
          ${switcherOpts}
        </select>
      </label>
      <div class="side-nav" role="navigation">
        ${appNav}
        <div class="nav-section">Platform</div>
        ${navLink("#/apps", "All apps", parts[0] === "apps" && !parts[1])}
        ${navLink("#/connections?provider=stripe", "Connections", parts[0] === "connections")}
        ${navLink("#/outbox", "Outbox", parts[0] === "outbox")}
        ${navLink("#/audit", "Audit log", parts[0] === "audit")}
      </div>
      <button type="button" class="logout" id="logout-btn">Log out</button>
    </aside>
    <div class="main">
      <div id="view"></div>
    </div>`;
}

/** @type {AbortController|null} */
let sidebarAbort = null;

const mobileNavMQ = window.matchMedia("(max-width: 800px)");

function teardownChrome() {
  if (sidebarAbort) {
    sidebarAbort.abort();
    sidebarAbort = null;
  }
  closeChoiceSheet();
  document.documentElement.classList.remove("nav-lock");
}

function switchApp(parts, appID) {
  if (!appID) {
    setCurrentAppID("");
    location.hash = "#/apps";
    return;
  }
  setCurrentAppID(appID);
  // Prefer keeping the current app section; from platform pages open Integrations.
  let section = "config";
  if (parts[0] === "apps" && parts[1]) section = sectionFromParts(parts);
  location.hash = hashForApp(appID, section);
}

function setSidebarInert(sidebar, inert) {
  if (!sidebar) return;
  sidebar.inert = inert;
  if (inert) sidebar.setAttribute("aria-hidden", "true");
  else sidebar.removeAttribute("aria-hidden");
}

function wireChrome(layout, parts, apps) {
  const sidebar = document.getElementById("sidebar");
  const openBtn = document.getElementById("sidebar-open");
  const closeBtn = document.getElementById("sidebar-close");
  const navBackdrop = document.getElementById("sidebar-backdrop");
  const appChip = document.getElementById("mobile-app-chip");
  if (!layout || !sidebar || !openBtn || !closeBtn || !navBackdrop) return;

  if (sidebarAbort) sidebarAbort.abort();
  sidebarAbort = new AbortController();
  const { signal } = sidebarAbort;

  const syncSidebarA11y = () => {
    // Off-canvas only on mobile; desktop sidebar stays interactive.
    const hide = mobileNavMQ.matches && !layout.classList.contains("layout--nav-open");
    setSidebarInert(sidebar, hide);
  };

  const setNavOpen = (open) => {
    layout.classList.toggle("layout--nav-open", open);
    openBtn.setAttribute("aria-expanded", open ? "true" : "false");
    navBackdrop.hidden = !open;
    if (open) closeChoiceSheet();
    syncSidebarA11y();
    syncChromeLock();
  };

  const closeOverlays = () => {
    closeChoiceSheet();
    setNavOpen(false);
  };

  setNavOpen(false);

  openBtn.addEventListener("click", () => setNavOpen(true), { signal });
  closeBtn.addEventListener("click", () => setNavOpen(false), { signal });
  navBackdrop.addEventListener("click", () => setNavOpen(false), { signal });
  layout.querySelectorAll(".side-nav a").forEach((a) => {
    a.addEventListener("click", () => setNavOpen(false), { signal });
  });

  appChip?.addEventListener("click", () => {
    const currentID = getCurrentAppID() || "";
    openAppSwitcher({
      apps,
      currentID,
      onSelect: (id) => switchApp(parts, id),
    });
  }, { signal });

  document.addEventListener("keydown", (ev) => {
    if (ev.key !== "Escape") return;
    if (layout.classList.contains("layout--choice-sheet-open")) closeChoiceSheet();
    else if (layout.classList.contains("layout--nav-open")) setNavOpen(false);
  }, { signal });

  const onViewportChange = () => {
    if (!mobileNavMQ.matches) closeOverlays();
    else syncSidebarA11y();
  };
  if (typeof mobileNavMQ.addEventListener === "function") {
    mobileNavMQ.addEventListener("change", onViewportChange, { signal });
  } else {
    // Safari < 14
    mobileNavMQ.addListener(onViewportChange);
    signal.addEventListener("abort", () => mobileNavMQ.removeListener(onViewportChange));
  }
}

async function renderShell(parts) {
  teardownChrome();
  const apps = await loadApps();
  root.innerHTML = `<div class="layout">${shellHTML(parts, apps)}</div>`;
  mainEl = document.getElementById("view");
  const layout = root.querySelector(".layout");
  wireChrome(layout, parts, apps);

  document.getElementById("logout-btn").addEventListener("click", () => {
    teardownChrome();
    clearToken();
    clearAppsCache();
    setCurrentAppID("");
    location.hash = "#/apps";
    boot();
  });

  const switcher = document.getElementById("app-switcher");
  switcher.addEventListener("change", () => switchApp(parts, switcher.value));

  hydrate(root);
}

function renderLogin() {
  teardownChrome();
  root.innerHTML = `
    <div class="login-wrap">
      <article>
        <header>
          <h1>Builtly admin</h1>
          <p class="muted">Enter the master admin token. Stored in <code>sessionStorage</code> for this tab only.</p>
        </header>
        <div id="login-error" hidden></div>
        <form id="login-form">
          <label>
            Admin token
            <input id="token" name="token" type="password" autocomplete="off" required>
          </label>
          <label>
            <input type="checkbox" id="show-token"> Show token
          </label>
          <div class="form-actions">
            <button type="submit">Continue</button>
          </div>
        </form>
      </article>
    </div>`;
  hydrate(root);
  document.getElementById("show-token").addEventListener("change", (ev) => {
    document.getElementById("token").type = ev.target.checked ? "text" : "password";
  });
  document.getElementById("login-form").addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const token = document.getElementById("token").value.trim();
    if (!token) return;
    setToken(token);
    try {
      await api("GET", "/apps?limit=1");
      boot();
    } catch {
      clearToken();
      // api() already toasted the HTTP error; keep an inline hint on the login card.
      const box = document.getElementById("login-error");
      if (box) {
        box.hidden = false;
        box.className = "banner";
        box.setAttribute("data-tone", "err");
        box.setAttribute("role", "alert");
        box.textContent = "Token rejected. Check BUILTLY_ADMIN_TOKEN.";
      }
    }
  });
}

async function route() {
  if (!getToken()) {
    renderLogin();
    return;
  }

  let parts = parseRoute();

  // App-first default: reopen last app when landing on bare #/ or unknown.
  if (!parts.length) {
    const last = getCurrentAppID();
    location.hash = last ? hashForApp(last, "overview") : "#/apps";
    return;
  }

  if (parts[0] === "apps" && parts[1]) {
    setCurrentAppID(parts[1]);
  }

  try {
    await renderShell(parts);
  } catch (e) {
    teardownChrome();
    root.innerHTML = `<p class="banner" data-tone="err" role="alert">${esc(e.message || String(e))}</p>`;
    return;
  }

  const view = mainEl;
  try {
    if (parts[0] === "connections") {
      await renderConnections(view);
      hydrate(view);
      return;
    }
    if (parts[0] === "outbox") {
      await renderOutbox(view);
      hydrate(view);
      return;
    }
    if (parts[0] === "audit") {
      await renderAudit(view);
      hydrate(view);
      return;
    }
    if (parts[0] === "apps") {
      const appID = parts[1];
      if (!appID) {
        clearAppsCache(); // list may create apps
        await renderAppsList(view);
        hydrate(view);
        return;
      }
      const section = parts[2] || "";
      if (section === "config") {
        await renderAppConfig(view, appID);
        hydrate(view);
        return;
      }
      if (section === "keys") {
        await renderKeys(view, appID);
        hydrate(view);
        return;
      }
      if (section === "products") {
        await renderProducts(view, appID);
        hydrate(view);
        return;
      }
      if (section === "users") {
        const uid = parts[3];
        if (uid) await renderUserDetail(view, appID, uid);
        else await renderUsers(view, appID);
        hydrate(view);
        return;
      }
      if (section === "referrals") {
        await renderReferrals(view, appID);
        hydrate(view);
        return;
      }
      if (section === "activity") {
        await renderActivity(view, appID);
        hydrate(view);
        return;
      }
      await renderAppDetail(view, appID);
      hydrate(view);
      return;
    }
    location.hash = "#/apps";
  } catch (e) {
    // API failures already toasted via onError; still replace the view so the page isn't blank.
    view.innerHTML = `<p class="banner" data-tone="err" role="alert">${esc(e.message || String(e))}</p>`;
    hydrate(view);
  }
}

function boot() {
  if (!getToken()) {
    renderLogin();
    return;
  }
  // First paint: prefer last selected app over empty apps index.
  if (!location.hash || location.hash === "#" || location.hash === "#/") {
    const last = getCurrentAppID();
    location.hash = last ? hashForApp(last, "overview") : "#/apps";
    return;
  }
  route();
}

window.addEventListener("hashchange", () => {
  if (getToken()) route();
});

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", boot);
} else {
  boot();
}
