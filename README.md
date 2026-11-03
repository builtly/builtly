<p align="center">
  <img src="docs/assets/icon.svg" alt="Builtly" width="160">
</p>

# Builtly

Open-source multi-tenant SaaS backend for apps and static sites — Authentication, Apple/Google IAP & Stripe billing, AI Gateway, Credits ledger, R2 file storage, Referrals, and Guest/Device migrations.

- **Architecture Specification**: [`docs/architecture.md`](docs/architecture.md) (normative spec, milestones M0–M14).
- **Client Integration Guide**: [`docs/client-jekyll.md`](docs/client-jekyll.md) (app ID, CORS, auth, products, Stripe checkout, credits).

---

## Highlights & Features

- **Multi-Tenant by Design**: Run multiple independent applications (`apps`) on a single instance with per-app settings, CORS policies, allowed redirect URLs, and storage quotas.
- **Authentication**:
  - Email + Password with secure token rotation, email verification, and password resets.
  - OAuth (Google, GitHub, Apple) with PKCE and mobile native Apple/Google Sign-In.
  - **Guest / Device Accounts**: Dedicated guest accounts (e.g. `guest_<deviceId>@<domain>`) with registration bonus guards and anti-bounce protections.
  - **App Review / IAP Sandbox Accounts**: Tester emails listed in `iap_sandbox_emails` can register or sign in with email/password (auto-provisioning and auto-verifying on first login) even when regular password login is disabled.
- **Billing & Monetization**:
  - **Stripe Checkout**: One-time credit packs and webhook lifecycle (`checkout.session.completed`, refunds, dispute chargebacks). Subscriptions and recurring plans are not in v1.
  - **Native Mobile IAP**: Apple App Store Server API (JWS transactions) & Google Play Developer API with receipt verification, anti-double-spend claim checks, and refund clawback poller.
  - **IAP Sandbox Governance**: Test apps accept all sandbox/test purchases with optional account tokens. Production apps reject sandbox by default and allow only designated tester emails with a required store account token (`appAccountToken` / `obfuscatedExternalAccountId`).
  - **Guest Credits Transfer**: Atomic, idempotent migration (`POST /billing/transfer-guest-credits`) moving remaining guest balance and IAP purchase history to authenticated accounts upon login.
- **AI Gateway & Credit Metering**:
  - OpenAI-compatible chat completions proxy (`POST /ai/chat/completions`) routing to upstream providers (OpenAI, Anthropic, DeepSeek, Grok, etc.).
  - Configurable pricing per app (per-request or per-1K tokens).
  - Strict append-only credit ledger (`credit_balances` & `credit_ledger`) ensuring balance invariants hold under high concurrency.
- **Object Storage**:
  - Cloudflare R2 integration with presigned direct uploads, user quota tracking, and automatic avatar processing.
- **Growth & Telemetry**:
  - Built-in referral codes, signup referee bonus credits, and reward attribution on purchases.
  - Transactional outbox event dispatcher publishing server-side lifecycle facts (`user_signed_up`, `user_logged_in`, `checkout_session_created`, `purchase_completed`, `purchase_refunded`, `credits_consumed`, `guest_credits_transferred`, `file_uploaded`) to PostHog and external affiliate platforms (PromoteKit, Affonso). Deliveries skip safely when analytics keys are not configured.
- **Industrial Admin Dashboard**:
  - Built-in browser management UI (`/admin/`) with dark/light themes, real-time analytics, user status & password management, audit logging, outbox queue inspector, connection secret vault, and visual app settings.

---

## Quick Start

```bash
cp .env.example .env   # leave PUBLIC_URL / DB / secrets empty to hit the setup wizard
go run ./cmd/builtly serve
```

- **Health Checks**: `GET http://localhost:8080/healthz` (liveness) & `/readyz` (readiness)
- **First-Boot Setup Wizard**: Open `http://localhost:8080/admin/#/setup` (setup token printed in logs or passed via `BUILTLY_SETUP_TOKEN`)
- **API Base**: `http://localhost:8080/api/v1` with `X-App-ID: <uuid>` + Bearer JWT or `bk_...` API key

### Configuration Hierarchy

**Process Environment Variables** → **Data Directory `builtly.json`** → **Defaults**

On startup, Builtly connects to the configured database:
- If connected and migrations are up-to-date → starts serving traffic.
- If database is unreachable or unconfigured → opens the interactive web setup wizard to configure SQLite or Postgres with secrets safely encrypted in AES-GCM vaults.

### Supported Databases

- **SQLite Local**: Zero external dependencies, single-node embedded storage.
- **SQLite Cloud / Turso (libSQL)**: Distributed SQLite for edge deployments.
- **PostgreSQL**: PostgreSQL 16+ (Neon, Supabase, Amazon RDS, or self-hosted).

### Docker Compose

```bash
# Start Builtly with SQLite on persistent volume at /data
docker compose up --build

# Run full test suite with isolated PostgreSQL test container
docker compose --profile test run --rm test
```

---

## Core API Endpoints

All client requests under `/api/v1/*` (except provider webhooks and OAuth redirects) require the `X-App-ID` header.

### 1. Auth & Identity

| Method | Endpoint | Description |
|---|---|---|
| `GET` | `/api/v1/auth/config` | Public app configuration (auth methods, client IDs, guest domain, quotas) |
| `POST` | `/api/v1/auth/register` | Register with email/password (auto-verifies guest & sandbox review accounts) |
| `POST` | `/api/v1/auth/login` | Password login (auto-provisions missing sandbox review accounts), issues short-lived access JWT + refresh token |
| `POST` | `/api/v1/auth/refresh` | Rotate refresh token family and get a new access token |
| `POST` | `/api/v1/auth/logout` | Revoke active refresh token family |
| `GET` | `/api/v1/auth/oauth/{provider}` | Start web OAuth PKCE flow (Google, GitHub, Apple) |
| `POST` | `/api/v1/auth/oauth/exchange` | Exchange PKCE authorization code for session tokens |
| `POST` | `/api/v1/auth/apple` | Mobile native Apple Sign-In exchange |
| `POST` | `/api/v1/auth/google` | Mobile native Google Sign-In exchange |

### 2. User & Credits

| Method | Endpoint | Auth | Description |
|---|---|---|---|
| `GET` | `/api/v1/user/me` | User | Get current profile, identities, and metadata |
| `PATCH` | `/api/v1/user/me` | User | Update display name, locale, or avatar URL |
| `DELETE` | `/api/v1/user/me` | User | Soft-delete account and anonymize identity records |
| `GET` | `/api/v1/user/credits` | User | Get current credit balance |
| `GET` | `/api/v1/user/credits/ledger` | User | Keyset-paginated ledger entries (`iap_purchase`, `ai_consume`, etc.) |
| `GET` | `/api/v1/user/referral` | User | Get user referral code and referee statistics |
| `POST` | `/api/v1/user/referral/apply` | User | Apply a friend's referral code |

### 3. Billing & IAP

| Method | Endpoint | Auth | Description |
|---|---|---|---|
| `GET` | `/api/v1/products` | Public | List available active credit packs and channel product IDs |
| `POST` | `/api/v1/checkout/stripe` | User | Create a Stripe Hosted Checkout Session |
| `POST` | `/api/v1/billing/verify-iap` | User | Verify Apple/Google native transaction and grant credits |
| `POST` | `/api/v1/billing/transfer-guest-credits` | User | Transfer guest remaining credits and reassign IAP purchases |
| `POST` | `/api/v1/webhooks/stripe/{connection_id}` | Webhook | Handle Stripe event notifications (refunds, dispute status) |
| `POST` | `/api/v1/webhooks/apple/{connection_id}` | Webhook | Handle Apple App Store Server Notifications V2 |

### 4. AI Gateway & Files

| Method | Endpoint | Auth | Description |
|---|---|---|---|
| `POST` | `/api/v1/ai/consume` | User / API Key | Manually deduct credits for custom AI operations |
| `POST` | `/api/v1/ai/chat/completions` | User / API Key | OpenAI-compatible chat proxy with automatic credit deduction |
| `POST` | `/api/v1/files` | User | Reserve an upload slot for Cloudflare R2 |
| `POST` | `/api/v1/files/{file_id}/complete` | User | Verify uploaded object and mark file ready |

---

## Guest / Device Account Flow

Builtly provides a first-class guest account migration workflow. Enable **guest email login** and set a guest email domain under App Settings → Authentication & Access. Guest register, login, and credit transfer stay off until that switch is on, including when regular password login is disabled.

```
[Unauthenticated Device]
       │
       ├─ 1. POST /api/v1/auth/register (guest_<deviceId>@device.example.com)
       │     └─ Auto-verified email; no welcome email; 0 signup bonus credits.
       │
       ├─ 2. In-App Purchase → POST /api/v1/billing/verify-iap
       │     └─ Credits granted to guest; transaction bound to guest UserID.
       │
[User Signs In with Apple/Google]
       │
       ├─ 3. Formal user registers/logs in and receives formal JWT.
       │
       └─ 4. POST /api/v1/billing/transfer-guest-credits
             Headers: Authorization: Bearer <formal_user_token>
             Body:    { "guest_access_token": "...", "guest_refresh_token": "..." }
             │
             ├─ Atomically moves guest balance to formal user via ledger.
             ├─ Reassigns IAP purchase history (enables correct refund clawbacks).
             └─ Subsequent transfer calls return 200 with already_transferred: true.
```

---

## Analytics & Tracking Architecture

Builtly follows a strict **backend for business facts, client for UI interactions** telemetry model:

| Layer | Responsibility | Typical Events | Mechanism |
|---|---|---|---|
| **Builtly (Server)** | Committed mutations & financial state changes | `user_signed_up`, `user_logged_in`, `checkout_session_created`, `purchase_completed`, `purchase_refunded`, `credits_consumed`, `guest_credits_transferred`, `file_uploaded` | Transactional outbox worker, deduplicated by outbox UUID, `distinct_id = user_id`. Safe no-op if no analytics key is configured. |
| **Web / Mobile Client** | UI impressions, user intent, client-side dropped flows | `$pageview`, paywall exposure, `checkout_clicked`, native sheet cancelled, network timeouts | Direct calls to client SDKs (PostHog, GA4, Firebase). |

**Identity Stitching Guideline**:
1. Before authentication or guest creation, the client SDK uses its own anonymous identifier.
2. Once an account is established (formal sign-in, OAuth, or automated guest registration), client apps call `identify(user.id)` using the Builtly user UUID to unify client impressions with server-side transactions.
3. Call `reset()` on logout. Do not duplicate server-side lifecycle events from front-end code.

---

## CLI & Operations

Builtly ships as a single Go binary with operation subcommands:

```bash
# Start the HTTP server and background workers (outbox, poller, storage cleaner)
builtly serve

# Run database schema migrations
builtly migrate up
builtly migrate status

# Check credit ledger balance invariants across all apps or a single app
builtly credits reconcile [--app <app-id>]

# Re-encrypt integration secrets and outbox payloads with new active key version
builtly secrets rotate

# Print version
builtly version
```

---

## Development & Testing

Builtly includes an extensive integration test suite covering race conditions, idempotency, webhook replay attacks, CSP headers, and multi-tenant isolation.

```bash
# Run all package tests
go test ./...

# Run race detector tests (requires CGO)
CGO_ENABLED=1 go test -race ./...

# Run specific domain tests
go test -v ./internal/billing -run TestTransfer
```

---

## License & Attribution

Builtly is licensed under the MIT License.

The Builtly gopher is inspired by the Go gopher, originally designed by [Renée French](https://reneefrench.blogspot.com/) and licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
