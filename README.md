<p align="center">
  <img src="docs/assets/icon.svg" alt="Builtly" width="160">
</p>

# Builtly

Open-source multi-tenant SaaS backend for apps and static sites—Auth, Stripe/IAP billing, AI credits, file storage, referrals, and growth hooks.

Architecture: [`docs/architecture.md`](docs/architecture.md) (normative). Milestones **M0–M14** in §5.11.

Client (Jekyll / static site): [`docs/client-jekyll.md`](docs/client-jekyll.md) — app id, CORS, auth, products, Stripe checkout, credits.

## Quick start

```bash
cp .env.example .env   # leave PUBLIC_URL / DB / secrets empty to hit the setup wizard
go run ./cmd/builtly serve
```

- Health: `GET http://localhost:8080/healthz`
- Admin UI: `http://localhost:8080/admin/#/setup` on first boot (token in logs / `BUILTLY_SETUP_TOKEN`)
- API base: `http://localhost:8080/api/v1` with `X-App-ID` + user JWT or `bk_…` API key

Config order: **process env → data-dir `builtly.json` → defaults**. After load, Builtly pings the DB; success → serve, failure → setup (prefilled, secrets preserved).

Databases in the wizard: **SQLite local** (single node), **SQLite cloud / Turso (libSQL)** (multi-node OK), **Postgres** (Neon / Supabase / self-hosted). Multi-node: use the wizard’s Copy env / Download `builtly.json` into platform Secrets on every machine.

```bash
docker compose up --build postgres builtly
# API http://localhost:8080  ·  Admin http://localhost:8080/admin/#/setup
docker compose --profile test run --rm test
```

### First admin steps

1. Open `/admin/` (setup wizard if unconfigured; otherwise paste the admin token).
2. Create an **app** (slug, allowed origins, signup bonus / referral settings as needed).
3. Create **connections** (Stripe, Resend, R2, OAuth, IAP, PostHog, …) or rely on env defaults.
4. Bind connections under the app **config**; create products and API keys.
5. Register a user against the app (`POST /api/v1/auth/register` with `X-App-ID`).

### Useful CLI

```bash
builtly serve
builtly migrate up | status
builtly secrets rotate
builtly credits reconcile [--app <id>]
builtly version
```

### Tests

```bash
go test ./...
CGO_ENABLED=1 go test -race ./...
```

Mandatory scenarios (consume race, webhook replay, IAP, referrals, secrets redaction, admin CSP, …) are listed in `docs/architecture.md` §5.9.

---

The Builtly gopher is inspired by the Go gopher, originally designed by [Renée French](https://reneefrench.blogspot.com/) and licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
