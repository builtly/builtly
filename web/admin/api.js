const TOKEN_KEY = "builtly_admin_token";
const BASE = "/api/v1/admin";

/** @type {((err: {code?: string, message?: string}|null) => void)|null} */
let errorHandler = null;
/** @type {((msg: string) => void)|null} */
let okHandler = null;

export function getToken() {
  return sessionStorage.getItem(TOKEN_KEY);
}

export function setToken(token) {
  sessionStorage.setItem(TOKEN_KEY, token);
}

export function clearToken() {
  sessionStorage.removeItem(TOKEN_KEY);
}

export function onError(fn) {
  errorHandler = fn;
}

export function onOk(fn) {
  okHandler = fn;
}

export function clearError() {
  if (errorHandler) errorHandler(null);
}

export function showOk(msg) {
  if (okHandler) okHandler(msg);
}

/**
 * Admin API wrapper: attaches Bearer token and surfaces apperr payloads.
 * @param {string} method
 * @param {string} path path under /api/v1/admin
 * @param {unknown} [body]
 */
export async function api(method, path, body) {
  clearError();
  const headers = { Authorization: "Bearer " + getToken() };
  const init = { method, headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const res = await fetch(BASE + path, init);
  if (res.status === 204) return null;
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
    const err = (data && data.error) || { code: "http_error", message: res.statusText || "request failed" };
    if (errorHandler) errorHandler(err);
    const e = new Error(err.message || "request failed");
    e.code = err.code;
    e.status = res.status;
    e.details = err.details;
    throw e;
  }
  return data;
}

export function esc(s) {
  return String(s ?? "")
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

export function fmt(v) {
  if (v == null) return "";
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/** Format an API timestamp as local `YYYY-MM-DD HH:mm:ss` (no Z/offset). */
export function fmtTime(v) {
  if (v == null || v === "") return "—";
  const d = v instanceof Date ? v : new Date(v);
  if (Number.isNaN(d.getTime())) return String(v);
  const p = (n) => String(n).padStart(2, "0");
  return (
    d.getFullYear() +
    "-" +
    p(d.getMonth() + 1) +
    "-" +
    p(d.getDate()) +
    " " +
    p(d.getHours()) +
    ":" +
    p(d.getMinutes()) +
    ":" +
    p(d.getSeconds())
  );
}

/** Re-scan el for Alpine directives after innerHTML updates (CSP Alpine build). */
export function hydrate(el) {
  if (typeof window !== "undefined" && window.Alpine && el) {
    window.Alpine.initTree(el);
  }
}
