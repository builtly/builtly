const APP_KEY = "builtly_admin_current_app";

/** @typedef {{ id: string, name: string, slug: string, status?: string }} AppSummary */

/** @type {AppSummary[]|null} */
let appsCache = null;

export function getCurrentAppID() {
  return sessionStorage.getItem(APP_KEY) || "";
}

export function setCurrentAppID(id) {
  if (id) sessionStorage.setItem(APP_KEY, id);
  else sessionStorage.removeItem(APP_KEY);
}

export function clearAppsCache() {
  appsCache = null;
}

/** @param {AppSummary[]} apps */
export function setAppsCache(apps) {
  appsCache = apps;
}

export function getAppsCache() {
  return appsCache;
}

/** Keep section when switching apps: overview|config|keys|products|email|ai|users|activity|referrals */
export function sectionFromParts(parts) {
  if (parts[0] !== "apps" || !parts[1]) return "overview";
  const s = parts[2] || "";
  if (!s) return "overview";
  if (s === "users" && parts[3]) return "users";
  return s;
}

export function hashForApp(appID, section) {
  if (!appID) return "#/apps";
  if (!section || section === "overview") return "#/apps/" + appID;
  return "#/apps/" + appID + "/" + section;
}
