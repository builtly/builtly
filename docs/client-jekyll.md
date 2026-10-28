# Client guide: Jekyll (static site)

Use this when wiring a Jekyll (or any static) site to Builtly. Normative API details live in [`architecture.md`](architecture.md) §4; this page is the practical path.

Builtly is **not** embedded in Jekyll. The site is a browser client that calls `https://<builtly-host>/api/v1/...` with your **app id** and a **user JWT**. Admin setup and Stripe/OAuth webhooks stay on the Builtly side.

## 1. Admin setup (once)

Do this in `/admin/` before writing front-end code.

1. **Create an app** — copy its UUID (`app_id`). It is public; you will put it in `_config.yml`.
2. **Allowed origins** — add every browser origin that will call the API, e.g. `http://127.0.0.1:4000`, `https://yoursite.com`. Missing → browser gets `403 origin_not_allowed`.
3. **Auth redirect URLs** — exact OAuth return URLs, e.g. `http://127.0.0.1:4000/auth/callback/`, `https://yoursite.com/auth/callback/`. Must match the `redirect_uri` query param character-for-character (trailing slash matters).
4. **Integrations** — bind Stripe (Checkout), Resend (mail), OAuth providers as needed; create credit-pack **Products** with `stripe_price_id` for web checkout.
5. Optional: enable password and/or Google / GitHub / Apple under app auth settings.

Local Builtly: `http://localhost:8080` (see root `README.md`). Production: your `BUILTLY_PUBLIC_URL`.

## 2. Jekyll config

```yaml
# _config.yml
builtly:
  api_base: "http://localhost:8080/api/v1"   # no trailing slash beyond /v1
  app_id: "0192…"                            # from admin → app
  auth_redirect: "/auth/callback/"           # site-relative; must be in auth_redirect_urls
  checkout_success: "/billing/thanks/"
  checkout_cancel: "/pricing/"
```

Expose them to JS (Liquid):

```html
<script>
  window.BUILTLY = {
    apiBase: {{ site.builtly.api_base | jsonify }},
    appId: {{ site.builtly.app_id | jsonify }},
    authRedirect: {{ site.builtly.auth_redirect | absolute_url | jsonify }},
    checkoutSuccess: {{ site.builtly.checkout_success | absolute_url | jsonify }},
    checkoutCancel: {{ site.builtly.checkout_cancel | absolute_url | jsonify }}
  };
</script>
<script type="module" src="{{ '/assets/js/builtly.js' | relative_url }}"></script>
```

## 3. Request rules (every call)

| Header | When |
|---|---|
| `X-App-ID: <app uuid>` | All `/api/v1/*` tenant routes |
| `Content-Type: application/json` | JSON bodies |
| `Authorization: Bearer <access_token>` | User routes (`/user/*`, checkout, files, …) |
| `Idempotency-Key` | Required on `POST /ai/consume` |

Error shape:

```json
{ "error": { "code": "insufficient_credits", "message": "…", "details": {}, "request_id": "…" } }
```

**Never** put an app API key (`bk_…`) or the admin token in Jekyll / browser bundles. API keys are for your **own** server (e.g. an AI proxy that calls `POST /ai/consume`). The static site only uses the user JWT.

## 4. Minimal browser client

Save as `assets/js/builtly.js` (adjust paths to your theme).

```js
const cfg = window.BUILTLY;
const TOKEN_KEY = "builtly_access";
const REFRESH_KEY = "builtly_refresh";

export function getAccess() { return sessionStorage.getItem(TOKEN_KEY); }
export function getRefresh() { return localStorage.getItem(REFRESH_KEY); }

export function saveTokens(tokens) {
  sessionStorage.setItem(TOKEN_KEY, tokens.access_token);
  localStorage.setItem(REFRESH_KEY, tokens.refresh_token);
}

export function clearTokens() {
  sessionStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(REFRESH_KEY);
}

export async function api(method, path, { body, auth = false, headers = {} } = {}) {
  const h = {
    "Content-Type": "application/json",
    "X-App-ID": cfg.appId,
    ...headers,
  };
  if (auth) {
    const t = getAccess();
    if (!t) throw Object({ code: "unauthorized", message: "not signed in" });
    h.Authorization = "Bearer " + t;
  }
  let res = await fetch(cfg.apiBase + path, {
    method,
    headers: h,
    body: body == null ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && auth && getRefresh()) {
    await refreshSession();
    h.Authorization = "Bearer " + getAccess();
    res = await fetch(cfg.apiBase + path, {
      method,
      headers: h,
      body: body == null ? undefined : JSON.stringify(body),
    });
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw data.error || { code: "http_" + res.status, message: res.statusText };
  return data;
}

async function refreshSession() {
  const data = await api("POST", "/auth/refresh", {
    body: { refresh_token: getRefresh() },
  });
  saveTokens(data.tokens);
  return data;
}

export async function register(email, password, displayName) {
  const data = await api("POST", "/auth/register", {
    body: { email, password, display_name: displayName || undefined },
  });
  saveTokens(data.tokens);
  return data;
}

export async function login(email, password) {
  const data = await api("POST", "/auth/login", { body: { email, password } });
  saveTokens(data.tokens);
  return data;
}

export async function logout() {
  const rt = getRefresh();
  clearTokens();
  if (rt) {
    try { await api("POST", "/auth/logout", { body: { refresh_token: rt } }); } catch { /* ignore */ }
  }
}
```

Access tokens are short-lived (~15 min). Refresh tokens last longer; **reuse of a refresh token revokes the whole family** — only call `/auth/refresh` once per expiry, and store the new pair.

## 5. Email auth (simplest for Jekyll)

**Register** → `POST /auth/register` → store tokens → optional verify mail flow (`POST /auth/email/verify/request` while logged in; user opens link / pastes token into `POST /auth/email/verify`).

**Login** → `POST /auth/login`.

Example form handler:

```js
import { login, register } from "./builtly.js";

document.querySelector("#login")?.addEventListener("submit", async (ev) => {
  ev.preventDefault();
  const fd = new FormData(ev.target);
  try {
    await login(String(fd.get("email")), String(fd.get("password")));
    location.href = "/account/";
  } catch (err) {
    alert(err.code + ": " + err.message);
  }
});
```

## 6. OAuth (Google / GitHub / Apple web)

Flow is PKCE between **your page** and Builtly (not only the IdP).

1. On the login page, generate `code_verifier` (43–128 chars) and `code_challenge = BASE64URL(SHA-256(verifier))`.
2. Keep `code_verifier` in `sessionStorage`.
3. Navigate the browser to:

   `GET {api_base}/auth/oauth/{google|github|apple}?app_id={appId}&redirect_uri={authRedirect}&code_challenge={challenge}&code_challenge_method=S256`

4. After consent, Builtly redirects to `redirect_uri?code=<login_code>` (or `?error=…`).
5. On `/auth/callback/`, `POST /auth/oauth/exchange` with `{ code, code_verifier }` and `X-App-ID`, then `saveTokens`.

```js
function b64url(buf) {
  return btoa(String.fromCharCode(...new Uint8Array(buf)))
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

export async function startOAuth(provider) {
  const verifier = b64url(crypto.getRandomValues(new Uint8Array(32)));
  sessionStorage.setItem("builtly_pkce", verifier);
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  const challenge = b64url(digest);
  const u = new URL(cfg.apiBase + "/auth/oauth/" + provider);
  u.searchParams.set("app_id", cfg.appId);
  u.searchParams.set("redirect_uri", cfg.authRedirect);
  u.searchParams.set("code_challenge", challenge);
  u.searchParams.set("code_challenge_method", "S256");
  location.href = u.toString();
}

// auth/callback/ page
export async function finishOAuth() {
  const params = new URLSearchParams(location.search);
  if (params.get("error")) throw { code: params.get("error"), message: "oauth failed" };
  const code = params.get("code");
  const verifier = sessionStorage.getItem("builtly_pkce");
  sessionStorage.removeItem("builtly_pkce");
  const data = await api("POST", "/auth/oauth/exchange", {
    body: { code, code_verifier: verifier },
  });
  saveTokens(data.tokens);
  return data;
}
```

Jekyll page stub:

```markdown
---
layout: default
permalink: /auth/callback/
---
<script type="module">
  import { finishOAuth } from "{{ '/assets/js/builtly.js' | relative_url }}";
  finishOAuth().then(() => { location.replace("/account/"); })
    .catch((e) => { document.body.textContent = e.code + ": " + e.message; });
</script>
```

Provider callback URL registered at Google/GitHub/Apple is **Builtly’s**, not your site:

`{BUILTLY_PUBLIC_URL}/api/v1/auth/oauth/{provider}/callback`

## 7. Account, credits, catalog, checkout

| Goal | Call |
|---|---|
| Profile | `GET /user/me` (auth) |
| Balance | `GET /user/credits` (auth) → `{ balance, updated_at }` |
| Ledger | `GET /user/credits/ledger?limit=50` (auth) |
| Sell packs | `GET /products` (only `X-App-ID`) |
| Pay (Stripe) | `POST /checkout/stripe` (auth) then redirect to `checkout_url` |

Checkout body:

```json
{
  "product_id": "0192…",
  "success_url": "https://yoursite.com/billing/thanks/?session_id={CHECKOUT_SESSION_ID}",
  "cancel_url": "https://yoursite.com/pricing/"
}
```

`success_url` / `cancel_url` origins must be in `allowed_origins`. After Stripe returns, poll `GET /user/credits` (webhook grants credits asynchronously).

```js
export async function buyProduct(productId) {
  const data = await api("POST", "/checkout/stripe", {
    auth: true,
    body: {
      product_id: productId,
      success_url: cfg.checkoutSuccess + (cfg.checkoutSuccess.includes("?") ? "&" : "?") + "session_id={CHECKOUT_SESSION_ID}",
      cancel_url: cfg.checkoutCancel,
    },
  });
  location.href = data.checkout_url;
}
```

IAP (`POST /billing/verify-iap`) is for native apps, not Jekyll.

## 8. AI credits (`/ai/consume`)

Two modes:

| Caller | Auth | `user_id` in body |
|---|---|---|
| Logged-in browser | User JWT | **Omit** (taken from token) |
| Your backend proxy | `Authorization: Bearer bk_…` | **Required** |

Jekyll pages usually should **not** call `/ai/consume` directly if the metering decision lives on a server you control. Typical pattern:

1. User is logged in on the static site (JWT).
2. Your AI backend receives the user JWT (or your own session), verifies identity, then calls Builtly with the **API key** and that `user_id`.
3. Always send a unique `Idempotency-Key` so retries do not double-charge.

Browser-only demo (user JWT):

```js
await api("POST", "/ai/consume", {
  auth: true,
  headers: { "Idempotency-Key": crypto.randomUUID() },
  body: { amount: 1, feature: "demo.ping", metadata: { page: location.pathname } },
});
```

`402 insufficient_credits` → show top-up UI (`GET /products` + checkout).

## 9. Suggested Jekyll pages

| Path | Purpose |
|---|---|
| `/account/login/` | Email + OAuth buttons |
| `/auth/callback/` | OAuth exchange (no layout chrome needed) |
| `/account/` | `GET /user/me` + balance |
| `/pricing/` | `GET /products` + Buy |
| `/billing/thanks/` | Post-checkout; refresh balance |

Gate private pages in JS: if `!getAccess()`, redirect to login. Jekyll cannot enforce auth at build time.

## 10. Local checklist

1. Builtly running; app created; `allowed_origins` includes `http://127.0.0.1:4000` (or whatever `jekyll serve` uses).
2. `_config.yml` `app_id` / `api_base` set.
3. Register → tokens in storage → `GET /user/credits` returns JSON.
4. Product + Stripe connection → checkout opens Stripe → after pay, balance increases.
5. OAuth: `auth_redirect_urls` exact match; provider callback points at Builtly.

## 11. Common failures

| Symptom | Likely cause |
|---|---|
| Browser CORS / `origin_not_allowed` | Origin missing from app `allowed_origins` |
| `missing_app_id` / `invalid_app_id` | Forgot or mistyped `X-App-ID` |
| `unauthorized` after login | Access token expired; refresh failed or refresh reused |
| OAuth 400 before redirect | `redirect_uri` not in `auth_redirect_urls` |
| `invalid_token` on exchange | Wrong/missing `code_verifier`, code expired (> ~2 min), or already used |
| Checkout `validation_failed` | success/cancel URL origin not allow-listed |
| `integration_not_configured` | Stripe (or other) connection not bound on the app |
| `insufficient_credits` | Need purchase or admin adjust |

## See also

- [`architecture.md`](architecture.md) §1.2 (tenancy / CORS), §3.2 (auth flows), §4 (endpoint contracts)
- Root [`README.md`](../README.md) — run Builtly, open `/admin/`
