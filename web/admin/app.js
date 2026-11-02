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
import { renderAppSettings } from "./views/app_settings.js";
import { renderConnections } from "./views/connections.js";
import { renderKeys } from "./views/keys.js";
import { renderProducts } from "./views/products.js";
import { renderEmail } from "./views/email.js";
import { renderUsers, renderUserDetail } from "./views/users.js";
import { renderReferrals } from "./views/referrals.js";
import { renderActivity } from "./views/activity.js";
import { renderAI } from "./views/ai.js";
import { renderOutbox } from "./views/outbox.js";
import { renderAudit } from "./views/audit.js";
import { fetchSetupStatus, renderSetup } from "./views/setup.js";

const root = document.getElementById("app");

/** @type {HTMLElement|null} */
let mainEl = null;

const GITHUB_URL = "https://github.com/builtly/builtly";
const GITHUB_ICON = `<svg class="gh-icon" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" focusable="false"><path fill="currentColor" d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>`;

/** @type {string} */
let appVersion = "dev";

async function loadVersion() {
  try {
    const res = await fetch("/healthz");
    if (!res.ok) return;
    const data = await res.json();
    if (data && data.version) appVersion = String(data.version);
  } catch {
    /* keep default */
  }
}

function githubLink(className, { label = true } = {}) {
  const text = label ? `<span>GitHub</span>` : "";
  const aria = label ? "" : ` aria-label="GitHub"`;
  return `<a class="${className}" href="${GITHUB_URL}" target="_blank" rel="noopener noreferrer"${aria}>${GITHUB_ICON}${text}</a>`;
}

/** Version + GitHub row (sidebar foot / login+setup meta). */
function buildMeta(variant) {
  const ver = esc(appVersion);
  if (variant === "sidebar") {
    return `<div class="sidebar__foot">
      <span class="sidebar__version" title="Builtly ${ver}">${ver}</span>
      ${githubLink("sidebar__github")}
    </div>`;
  }
  return `<div class="login-meta">
    <span class="login-meta__version" title="Builtly ${ver}">${ver}</span>
    ${githubLink("login-github")}
  </div>`;
}

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
      ${navLink(hashForApp(current.id, "overview"), "Overview", inApp && (section === "overview" || section === "settings" || section === "config"))}
      ${navLink(hashForApp(current.id, "users"), "Users", inApp && section === "users")}
      ${navLink(hashForApp(current.id, "activity"), "Activity", inApp && section === "activity")}
      ${navLink(hashForApp(current.id, "products"), "Products", inApp && section === "products")}
      ${navLink(hashForApp(current.id, "referrals"), "Referrals", inApp && section === "referrals")}
      ${navLink(hashForApp(current.id, "ai"), "AI", inApp && section === "ai")}
      ${navLink(hashForApp(current.id, "email"), "Email", inApp && section === "email")}
      ${navLink(hashForApp(current.id, "keys"), "API keys", inApp && section === "keys")}
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
        <img class="mobile-bar__logo" src="/admin/icon.svg" alt="" width="22" height="22">
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
          <img class="brand__logo" src="/admin/icon.svg" alt="" width="36" height="36">
          <div class="brand__text">
            <span class="brand__name">Builtly</span>
            <span class="brand__mark">admin</span>
          </div>
        </div>
        <div class="sidebar__head-actions">
          <button type="button" class="sidebar__close" id="sidebar-close" aria-label="Close menu">✕</button>
        </div>
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
        ${navLink("#/connections?provider=google_oauth", "Connections", parts[0] === "connections")}
        ${navLink("#/outbox", "Outbox", parts[0] === "outbox")}
        ${navLink("#/audit", "Audit log", parts[0] === "audit")}
      </div>
      <button type="button" class="logout" id="logout-btn">Log out</button>
      ${buildMeta("sidebar")}
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
      <div class="login-panel" data-reveal="1">
        <aside class="login-panel__rail" aria-hidden="true">ACCESS</aside>
        <div class="login-panel__body">
          <p class="user-kicker">Platform · access</p>
          <div class="login-brand">
            <img class="login-brand__logo" src="/admin/icon.svg" alt="" width="48" height="48">
            <div class="login-brand__text">
              <span class="login-brand__name">Builtly</span>
              <span class="login-brand__mark">admin</span>
            </div>
          </div>
          <p class="login-lede">Master token unlocks this ops console. Kept in <code>sessionStorage</code> for this tab only.</p>
          <div id="login-error" class="login-error" hidden></div>
          <form id="login-form" class="login-form">
            <label class="login-form__label" for="token">Admin token</label>
            <input id="token" name="token" type="password" autocomplete="off" required spellcheck="false">
            <label class="login-form__show">
              <input type="checkbox" id="show-token">
              <span>Show token</span>
            </label>
            <div class="login-form__footer">
              <button type="submit" class="login-form__submit">Continue</button>
            </div>
          </form>
          ${buildMeta("login")}
        </div>
      </div>
    </div>`;
  hydrate(root);
  const panel = root.querySelector(".login-panel");
  if (panel) requestAnimationFrame(() => panel.classList.add("login-panel--in"));
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
      // api() already toasted the HTTP error; keep an inline hint on the login panel.
      const box = document.getElementById("login-error");
      if (box) {
        box.hidden = false;
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
      if (section === "settings") {
        await renderAppSettings(view, appID);
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
      if (section === "email") {
        await renderEmail(view, appID);
        hydrate(view);
        return;
      }
      if (section === "ai") {
        await renderAI(view, appID);
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

async function boot() {
  await loadVersion();
  try {
    const status = await fetchSetupStatus();
    if (status.needs_setup || location.hash === "#/setup") {
      await renderSetup(root, {
        version: appVersion,
        onConfigured: () => {
          location.hash = "#/apps";
          boot();
        },
      });
      return;
    }
  } catch {
    // status endpoint missing on older builds — fall through to login
  }

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
  if (location.hash === "#/setup") {
    boot();
    return;
  }
  if (getToken()) route();
});

if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", () => {
    boot();
  });
} else {
  boot();
}
