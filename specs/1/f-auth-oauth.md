# Auth OAuth (v1) — open

## Overview

HTTP auth layer for the nanoclaw web UI. Supports local accounts and OAuth
providers. Issues short-lived JWTs + long-lived HttpOnly refresh tokens.

---

## Identity Providers

Implemented via [arctic](https://arcticjs.dev) — lightweight OAuth2/OIDC
library with no framework dependency.

| Provider | Mechanism         | Sub prefix | PKCE |
| -------- | ----------------- | ---------- | ---- |
| Local    | username + argon2 | `local:`   | n/a  |
| Telegram | Login Widget      | `tg:`      | n/a  |
| Discord  | OAuth2            | `discord:` | yes  |
| GitHub   | OAuth2            | `gh:`      | no   |
| Google   | OAuth2 + OIDC     | `google:`  | yes  |

GitHub does not support PKCE. Telegram uses its own widget flow, not OAuth2.

New providers: add an arctic adapter, register a route pair
(`/auth/<provider>` + `/auth/<provider>/callback`), no other changes.

---

## Token Model

**Access token** — JWT, 1hr TTL, stored in `localStorage`.

```json
{
  "sub": "tg:123456",
  "name": "Alice",
  "provider": "telegram",
  "exp": 1234567890
}
```

**Refresh token** — opaque random string (32 bytes, crypto.randomBytes),
30d TTL. Stored as SHA-256 hash in DB (high-entropy token, argon2id not
needed). Delivered via `Set-Cookie: refresh=<token>; HttpOnly; SameSite=Strict; Secure; Path=/auth`.

**Rotation**: each `POST /auth/refresh` call invalidates the old refresh
token and issues a new one (one-time-use rotation).

**Auto-refresh**: client checks JWT expiry on page focus / before fetch.
When `exp - now < 5min`, silently calls `POST /auth/refresh`. On 401 →
redirect to `/auth/login`.

---

## Routes

```
GET  /auth/login                   login page (server-rendered HTML)
POST /auth/login                   verify local credentials → mint tokens
GET  /auth/<provider>              redirect to OAuth provider
GET  /auth/<provider>/callback     verify state + code → mint tokens → redirect
POST /auth/token                   exchange one-time code for JWT
POST /auth/telegram/callback       verify Telegram widget payload → mint tokens
POST /auth/refresh                 exchange refresh cookie → new JWT + new cookie
POST /auth/logout                  delete session row + clear cookie
```

### GET /auth/login

Server-rendered HTML by the gateway router (not Vite SPA). Returns a plain
HTML page with a username/password form and OAuth provider buttons. No
JavaScript framework required — static HTML + minimal inline JS for
auto-refresh only.

### POST /auth/login

Request body (JSON or form):

```json
{ "username": "alice", "password": "hunter2" }
```

Response on success:

```json
{ "token": "<jwt>" }
```

Plus `Set-Cookie: refresh=<token>; HttpOnly; SameSite=Strict; Secure; Path=/auth`.

Rate limited: 5 attempts / 15 min per IP. Exceed → 429.

### GET /auth/<provider>

Generates PKCE verifier (Discord, Google only). Generates HMAC-signed state
cookie (see Security). Redirects to provider authorization URL.

### GET /auth/<provider>/callback

1. Validates state via HMAC cookie verification.
2. Exchanges code for tokens via arctic (with PKCE verifier for Discord/Google).
3. Resolves or creates user sub.
4. Mints JWT + refresh token.
5. Stores a one-time code (OTP): `crypto.randomBytes(16).toString('hex')`,
   keyed in memory, 60s TTL, single-use.
6. Redirects to `/?code=<otp>`.

Client JS on `/?code=<otp>` POSTs to `POST /auth/token?code=<otp>` to
exchange for the JWT, then stores it in `localStorage`.

### POST /auth/token

Query param: `code=<otp>`. Looks up OTP in memory store. If valid and
not expired: delete entry, respond with `{ "token": "<jwt>" }`. On failure: 400.

### POST /auth/telegram/callback

Browser POSTs the Telegram Login Widget payload:

```json
{
  "id": 123,
  "first_name": "Alice",
  "username": "alice",
  "hash": "...",
  "auth_date": 1234567890
}
```

Server verifies: `hash == HMAC-SHA256(sorted_data_check_string, SHA256(bot_token))`.
Also checks `auth_date` is within 5 minutes. On success, mints JWT + sets
refresh cookie. Returns `{ "token": "<jwt>" }`.

`TELEGRAM_BOT_TOKEN` env var required to enable this route.

### POST /auth/refresh

Reads `refresh` cookie. Computes SHA-256 of token. Looks up hash in
`auth_sessions`. If valid and not expired: delete row, insert new row,
respond with new JWT + new cookie. On any failure: clear cookie, return 401.

### POST /auth/logout

Deletes `auth_sessions` row for the presented refresh cookie hash.
Clears cookie. Returns 200.

---

## DB Schema

```sql
CREATE TABLE auth_users (
  id         INTEGER PRIMARY KEY,
  sub        TEXT UNIQUE NOT NULL,     -- e.g. "local:<uuid4>"
  username   TEXT UNIQUE NOT NULL,
  hash       TEXT NOT NULL,            -- argon2id hash of password
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE auth_sessions (
  token_hash TEXT PRIMARY KEY,         -- SHA-256 hex of refresh token
  user_sub   TEXT NOT NULL,            -- e.g. "tg:123456" or "local:<uuid4>"
  expires_at TEXT NOT NULL,            -- ISO-8601
  created_at TEXT NOT NULL
);
```

Local user `sub` is `local:<uuid4>` generated at registration (not row ID,
not username). No `auth_sessions` row for OAuth users on first login — sub
is derived from provider identity. A row is created on every refresh token
issuance regardless of provider.

---

## Security

- **Password hashing**: argon2id via `@node-rs/argon2`. Parameters: m=65536, t=3, p=4.
- **Refresh token storage**: SHA-256 hex of the raw token stored in DB. Raw
  token sent to client only once. SHA-256 is sufficient for high-entropy
  random tokens (32 bytes). Argon2id reserved for passwords.
- **JWT signing**: HS256, secret from `AUTH_SECRET` env var (min 32 bytes).
- **Cookie flags**: `HttpOnly; SameSite=Strict; Secure; Path=/auth`.
  `Path=/auth` limits cookie transmission to auth endpoints only.
- **OAuth state**: HMAC-signed cookie (stateless). Value = `HMAC-SHA256(AUTH_SECRET, nonce + timestamp)`.
  10min expiry. Verified on callback by recomputing HMAC.
- **PKCE**: S256 method, used for Google and Discord. Not used for GitHub
  (unsupported). Not applicable to Telegram.
- **Token rotation**: refresh tokens are single-use. Old token invalidated on
  each refresh.
- **Rate limiting**: in-memory sliding window on `POST /auth/login`. Keyed by
  `X-Forwarded-For` (first IP) or `req.socket.remoteAddress`.
- **Expiry sweep**: expired `auth_sessions` rows can be pruned on login or via
  a periodic task. No hard requirement — they are inert after `expires_at`.

---

## Config

| Env var                 | Required | Description                                 |
| ----------------------- | -------- | ------------------------------------------- |
| `AUTH_SECRET`           | yes      | JWT signing + HMAC state secret (≥32 bytes) |
| `TELEGRAM_BOT_TOKEN`    | no       | enables Telegram Login Widget               |
| `DISCORD_CLIENT_ID`     | no       | enables Discord OAuth                       |
| `DISCORD_CLIENT_SECRET` | no       |                                             |
| `GITHUB_CLIENT_ID`      | no       | enables GitHub OAuth                        |
| `GITHUB_CLIENT_SECRET`  | no       |                                             |
| `GOOGLE_CLIENT_ID`      | no       | enables Google OAuth                        |
| `GOOGLE_CLIENT_SECRET`  | no       |                                             |
| `AUTH_BASE_URL`         | no       | OAuth callback base (default: WEB_HOST)     |

Local auth is always enabled if `AUTH_SECRET` is set.

---

## Implementation Notes

- Auth routes live in `src/auth.ts`, exported as an Express router.
- `src/auth-middleware.ts` exports `requireAuth(req, res, next)` — verifies
  JWT from `Authorization: Bearer <token>` header. Used on non-public routes.
- REDACTED public routes (`/pub/`) bypass auth middleware entirely.
- `arctic` is the only new dependency. `@node-rs/argon2` for hashing.
- JWT lib: `jose` (already likely present; zero-dep, ESM-native).
- OTP store: in-memory `Map<string, { jwt: string; exp: number }>`, pruned on access.

---

## Slink sender identity

When a slink POST carries a valid JWT, the sender fields on the dispatched
message are derived from the JWT payload — not hardcoded to `'web'`:

```typescript
sender = jwt.sub; // e.g. "tg:123456" or "local:abc-uuid"
sender_name = jwt.name; // e.g. "Alice"
```

Unauthenticated slink posts (no JWT) use:

```typescript
sender = `anon:${anonCookieId}`; // stable per-browser cookie
sender_name = 'guest';
```

This means the agent sees the same `sender` value whether the user messages
via Telegram or via a slink link (if they logged in with Telegram OAuth).
Cross-channel identity linking in v2 — see `specs/3/9-identities.md`.

---

## Success Criteria

E2e tests must assert:

- **Local login**: `POST /auth/login` with valid credentials → `{ token }` in
  response body + `refresh` HttpOnly cookie set.
- **OAuth flow**: `GET /auth/github` → redirect to GitHub → callback →
  redirect to `/?code=<otp>` → `POST /auth/token?code=<otp>` → JWT issued.
- **Telegram**: `POST /auth/telegram/callback` with valid widget payload →
  `{ token }` + refresh cookie set.
- **Refresh**: `POST /auth/refresh` with valid cookie → new JWT in body +
  new refresh cookie replacing old.
- **Logout**: `POST /auth/logout` → refresh cookie cleared (Set-Cookie with
  expired date).
