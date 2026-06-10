---
status: shipped
---

# authd — the `/v1/*` token API + key lifecycle

The wire surface of `authd` (the daemon + offline-verify model is
[`1-auth-standalone.md`](1-auth-standalone.md)): the machine token/key
endpoints under `/v1/*`, the JWK rotation mechanics behind the emergency-revoke
lever, the TTL table, and service-token bootstrap. The human OAuth `/auth/*`
browser flow and the `auth/` library surfaces stay in `1-auth-standalone.md`.

## `/v1/*` API surface

**DECISION (route naming).** Machine token + key endpoints live under
`/v1/*` (consumed via the `auth/` library, not a separate client package);
the human OAuth browser
flow keeps `/auth/*` (proxyd 302s to `authd_url/auth/login` —
[`35-proxyd-standalone.md`](35-proxyd-standalone.md) § Login flow). The two
prefixes don't overlap.

All `/v1/*` JSON errors use `{"error":"<code>","message":"<human>"}` with the
HTTP status carrying the class. `GET /v1/keys` is **public** (mounted before
auth middleware); every other `/v1/*` endpoint requires a bootstrap secret
(`/v1/service-token`) or a bearer token.

### `GET /v1/keys` — JWK Set (public)

Marshalled by go-jose from `signing_keys` rows that are `active` OR within
their overlap window (`now < retired_at + max access TTL`). Purely
time-based — no flag. An emergency-revoked kid is retired with the overlap
already elapsed (below), so it falls out of this set immediately and ages
out via the normal GC.

```jsonc
// 200
{
  "keys": [
    {
      "kty": "EC",
      "crv": "P-256",
      "alg": "ES256",
      "use": "sig",
      "kid": "1735000000-a1b2c3d4",
      "x": "<b64url>",
      "y": "<b64url>",
    },
  ],
}
```

Cache headers `Cache-Control: public, max-age=3600` (= JWKS cache TTL).
Verifiers also refresh on `kid`-miss (one re-fetch before failing) — go-oidc
`RemoteKeySet` behavior, as-is.

### `POST /v1/tokens` — mint (bearer or service required)

One endpoint, two modes picked by whether the caller holds `tokens:mint` AND
requested `sub` ≠ caller `sub` (issuer mint) vs same `sub` (downscope). No
separate `/v1/downscope`. The two modes bound the minted scope against
**different sources** — that's the only authority rule, stated per-mode below.

- **Issuer mint** (caller has `tokens:mint`; onbod, dashd, proxyd-on-login):
  mints a fresh `user`/`service` token for a **different** `sub`. The minted
  `scope` is bounded by the **target sub's grants snapshot** — `authd` fetches
  `GET <GRANTS_URL>/v1/users/{sub}/scopes` for the requested (bare) `sub` and
  requires the requested `scope` ⊆ that snapshot, folder within the snapshot's
  `folder` subtree. It is **not** bounded by the caller's own scope: login,
  onbod, and dashd mint USER tokens for accounts whose grants the minter does
  not itself hold. The caller's `tokens:mint` is the authority to mint _at
  all_; the target's grants set the ceiling. An invite mints `user`, never
  `service`. Delegation, never escalation. Violation → `403 scope_exceeds_minter`.
- **Downscope** (any valid bearer, no `tokens:mint`): mints a `downscoped`
  token for the **same** `sub` (the `sub` field is forced to the caller's),
  narrowing the **presented parent token** — minted `scope` ⊆ the **caller's**
  `scope`, folder within the caller's `arz/folder` subtree, `parent_jti` =
  caller's `jti`, TTL capped at parent's remaining lifetime. Violation → `403
scope_exceeds_parent`.

```jsonc
// POST /v1/tokens  Authorization: Bearer <caller>
{ "typ":"user"|"service"|"downscoped", "sub":"...", "scope":["tasks:write"],
  "folder":"atlas/main", "audience":"", "ttl_seconds": 900 }
// 200
{ "token":"<jws>", "jti":"...", "expires_at":"2026-05-29T12:15:00Z" }
// 403 {"error":"scope_exceeds_parent"|"scope_exceeds_minter","message":"..."}
// 400 {"error":"invalid_scope","message":"global *:* not allowed"}
```

### `POST /v1/service-token` — service-token exchange

A daemon exchanges its bootstrap secret for a short-lived service JWT. The
secret goes in the `Authorization` header (not the body — keeps it out of
body-logging); daemon identity in the body. `authd` looks up `service_keys`
by `daemon`, compares the hash in constant time, signs a `service` token with
that row's `scope`.

```jsonc
// POST /v1/service-token  Authorization: Bearer <AUTHD_SERVICE_KEY>
{ "daemon":"timed" }
// 200
{ "token":"<jws>", "expires_at":"2026-05-29T13:00:00Z",
  "scope":["messages:write","tasks:read"] }
// 401 {"error":"bad_service_key","message":"..."}  (unknown daemon or hash mismatch)
```

The `auth.ServiceToken` helper (library) calls this at startup and
refreshes ~1 min before `expires_at`, the way `RemoteKeySet` refreshes —
no per-request hop. The returned token's `sub` is `service:<daemon>`.

### `POST /v1/refresh` — silent access-token refresh

Consumes a refresh token, rotates it (§ Sessions), returns a new access JWT
plus the successor refresh token by the **same channel it was presented on**:
browser (cookie) → successor in `Set-Cookie`, omitted from JSON body (stays
`HttpOnly`); non-browser (JSON body) → successor in JSON body, no `Set-Cookie`.

```jsonc
// Browser:    POST /v1/refresh   (cookie refresh_token=<opaque>)
// 200  Set-Cookie: refresh_token=<new opaque>; HttpOnly; ...
{ "token":"<access jws>", "expires_at":"2026-05-29T12:15:00Z" }

// Non-browser: POST /v1/refresh   {"refresh_token":"<opaque>"}
// 200  (no Set-Cookie)
{ "token":"<access jws>", "expires_at":"2026-05-29T12:15:00Z",
  "refresh_token":"<new opaque>" }

// 401 {"error":"invalid_refresh","message":"..."}  (missing/expired/reused → family invalidated)
```

If a request presents both a cookie and a body token, the cookie wins
(browser channel) and the body token is ignored.

### `POST /v1/keys/rotate` — rotate the signing key (operator)

> Deferred: the endpoint + `authd rotate-key` CLI are unbuilt. The rotation
> MECHANISM (§ JWK rotation) is specced and the only emergency-revoke lever;
> until the endpoint lands, short-TTL expiry + a redeploy with a fresh key
> rotate it.

Bearer with scope `keys:rotate`. Generates a new ES256 key, makes it
`active`, retires the old (§ JWK rotation). HTTP equivalent of `authd
rotate-key`.

```jsonc
// POST /v1/keys/rotate  Authorization: Bearer <operator>
{ "revoke_old": false }   // true = emergency: retire old kid with overlap already elapsed → dropped from JWK Set now
// 200
{ "new_kid":"1735003600-9f8e7d6c", "retired_kid":"1735000000-a1b2c3d4",
  "old_servable_until":"2026-06-28T12:00:00Z" }  // null when revoke_old=true
// 403 {"error":"forbidden","message":"keys:rotate required"}
```

### OAuth / login routes (browser, `/auth/*`)

Ported from `web.go`/`oauth.go`/`routes.go` into `authd`, minting ES256.

| Route                       | Method | Params / body                        | Result                                                                                                     |
| --------------------------- | ------ | ------------------------------------ | ---------------------------------------------------------------------------------------------------------- |
| `/auth/login`               | GET    | `?return=<rel-path>` `?error=`       | HTML login page (provider buttons). For local-password: also `POST`.                                       |
| `/auth/login`               | POST   | form `username`,`password`           | Verify argon2id; on success → `issueSession` (sets refresh cookie, returns access JWT).                    |
| `/auth/<provider>`          | GET    | `?intent=link` `?return=`            | Insert `oauth_state` row, redirect to provider authorize URL (PKCE S256 + nonce).                          |
| `/auth/<provider>/callback` | GET    | `?code` `?state`                     | Validate state row, exchange code (x/oauth2), verify ID token (go-oidc), resolve identity → dispatchOAuth. |
| `/auth/telegram`            | POST   | Telegram widget form                 | Verify widget HMAC, resolve `telegram:<id>` → dispatchOAuth.                                               |
| `/auth/collide`             | POST   | form `token`,`choice=link\|logout`   | Resolve a two-provider collision (§ Account linking).                                                      |
| `/auth/logout`              | POST   | cookie `refresh_token`               | Revoke the refresh token's family (set `revoked_at`), clear cookie, 303 → `/auth/login`.                   |
| `/auth/me`                  | GET    | `Authorization: Bearer <access jwt>` | `200 {"sub":"...","name":"...","scope":[...],"folder":"...","expires_at":"..."}`; 401 if no valid token.   |

- `<provider>` ∈ `{google, github, discord}`, mounted only when its client-id
  config is set (conditional registration in `routes.go`).
- `issueSession` (ported from `web.go`): canonicalize sub, snapshot scopes
  from grants (§ Login-time scope snapshot), mint the access JWT, create a
  `refresh_tokens` row, set the `HttpOnly` cookie, return the access JWT via
  the `localStorage` bootstrap HTML.
- **Response by `Accept`.** Browser (`text/html`): 302/callback complete with
  the `localStorage` bootstrap HTML, refresh token in the `HttpOnly` cookie.
  Programmatic (`application/json`): `200
{"token":"<jws>","expires_at":...,"refresh_token":"<opaque>"}`, the initial
  refresh token in the body (no cookie jar). Refresh token via cookie
  (browser) **or** body (non-browser), never both, on initial login and
  refresh alike. `/auth/me` reads the bearer, never the cookie.
- `return` validated as a relative path (`safeReturn`); `POST /auth/login`
  per-IP rate limit (`loginLimiter`, 5/15 min) carries forward.

### Login-time scope snapshot

`authd` does not own grants — routd does
([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md) § scope minter). At session
issuance `authd` fetches the caller's scope over one HTTP call:

```
GET <GRANTS_URL>/v1/users/{sub}/scopes      Authorization: Bearer <authd service token>
→ 200 {"scope":["messages:send","tasks:read","groups:read:own_group"], "folder":"atlas/main"}
→ 404 {"error":"no_grants"}   (sub has no grant rows)
```

- `{sub}` is the **bare** canonical sub (no `user:`/`service:` prefix —
  § JWT claim set "`sub` prefix rule"). Same call backs issuer-mint's
  ceiling check (§ `POST /v1/tokens`).
- Authenticated with `authd`'s own `service:authd` token (scope
  `grants:read`). `authd` **self-mints** this token: it holds the signing
  key, so it signs a `service:authd` JWT (`scope:["grants:read"]`, normal
  service TTL) directly at startup and refreshes it like any service token —
  no `service_keys` seed row, no bootstrap secret (the seed mechanism is for
  **other** daemons, which lack the key). authd is the one daemon that needs
  no bootstrap to obtain a service identity.
- Returned `scope` + `folder` stamped into the access JWT as `scope` /
  `arz/folder`. Snapshot taken **once at issuance**; later grant changes take
  effect only at next refresh/login (short-TTL model).
- **Failure / default**: `404 no_grants` → mint an **empty-scope** session,
  no `arz/folder` (authenticated but unauthorized; browser → `/onboard`). A
  5xx → `503 grants_unavailable`, login **fails closed** (no token minted)
  rather than masking the outage with an empty-scope session.
- `POST /v1/refresh` re-runs the snapshot so a refreshed token reflects
  current grants.

`GRANTS_URL` defaults to routd; a standalone `authd` deployment without
arizuko grants sets it to its own static-scope stub or omits it (then
every session is empty-scope, suitable for an auth-only deployment that
authorizes elsewhere).

## JWK rotation mechanics

- **`kid` scheme**: `"<created-unix>-<8 hex rand>"` (sortable by
  creation, collision-resistant). Written into every signed token's JWS
  header.
- **Startup-if-missing**: on boot, if no `active` row exists in
  `signing_keys`, `authd` generates an ES256 keypair, inserts it
  `active=1`, and signs with it. First boot needs no operator step.
- **Rotation cadence**: scheduled (default every 30 days, configurable
  via `AUTHD_KEY_ROTATION_DAYS`) **and** manual (`authd rotate-key` CLI /
  `POST /v1/keys/rotate` operator endpoint, scope `keys:rotate`).
- **Overlap window**: rotation inserts a new key `active=1`, sets the old
  `active=0, retired_at = now`. Both public keys stay in `GET /v1/keys` until
  `retired_at + max(access TTL, JWKS cache TTL)`, so old-`kid` tokens verify
  until they'd have expired anyway; GC drops the row after. The retired key's
  private half can be zeroed at retirement (never re-signed with).
- **Compromise = emergency revoke** (§ Revocation): `authd rotate-key
--revoke-old` rotates to a fresh active key AND retires the compromised key
  with **zero overlap** — `retired_at = now − (max access TTL)`, so the
  `now < retired_at + overlap` serve check is already false and the kid
  drops from `GET /v1/keys` immediately (then ages out via the normal GC, no
  permanent flag). Every token signed by it fails
  within one JWKS cache TTL (≤1 h; force-purge by restarting verifiers). The
  single lever that invalidates everything at once.

## TTL table

Defaults; `AUTHD_*` overrides each.

| Token / cache         | Default      | Env override              | Notes                                                |
| --------------------- | ------------ | ------------------------- | ---------------------------------------------------- |
| Access JWT            | 15 min       | `AUTHD_ACCESS_TTL`        | Verified offline; the revocation blast-radius bound. |
| Refresh token         | 30 days      | `AUTHD_REFRESH_TTL`       | Rotating, one-time-use; deletable on logout.         |
| Service token         | 1 hour       | `AUTHD_SERVICE_TTL`       | Library refreshes ~1 min before expiry.              |
| Downscoped token      | ≤ parent exp | (request `ttl_seconds`)   | Capped at the parent token's remaining lifetime.     |
| OAuth state           | 10 min       | `AUTHD_STATE_TTL`         | `oauth_state.expires_at`; GC-swept.                  |
| JWKS cache (verifier) | 1 hour       | `AUTHD_JWKS_CACHE_TTL`    | + refresh-on-`kid`-miss (go-oidc RemoteKeySet).      |
| Key rotation cadence  | 30 days      | `AUTHD_KEY_ROTATION_DAYS` | Scheduled; manual rotate always available.           |

## Service bootstrap

Daemon-initiated work (`timed` firing a task, `onbod` admitting, a cron
sweep) has no user in the loop but needs an identity to call routd: a
**service identity** with `sub = service:<daemon>` and the daemon's
capability scope. It verifies exactly like a user token — no second path.

- **Secret generation**: compose generation (`compose/compose.go`) writes one
  random `AUTHD_SERVICE_KEY` per daemon into its compose env and seeds
  `service_keys` with `(daemon, sha256(secret), scope)`. The secret is never
  shared; `authd` stores only its hash.
- **Capability source of truth**: each daemon's `service_scope =
["messages:write","tasks:read"]` is declared in its
  `template/services/<daemon>.toml`, aggregated by `compose.go` into the seed
  (no edit to `compose.go` for a new daemon's scope).
- **Binding**: the exchange is authenticated by the secret (header) and
  identified by `daemon` (body); `authd` binds via the `service_keys` row. A
  leaked key buys exactly that **one** daemon's scoped token — never the
  ability to sign, since only `authd` holds the private key.
- **Rotation**: re-generate compose (updates env secret + `service_keys`
  hash), restart the daemon; `service_keys.rotated_at` records it.

```go
// auth/ library, inside a daemon. Exchanges the bootstrap secret for a
// short-lived service JWT and keeps it refreshed; the daemon presents
// the returned token on daemon→daemon calls; it never holds a signing key.
func ServiceToken(authdURL, daemon, bootstrapKey string) (*TokenSource, error)
```

Bootstrap secrets are the **only** symmetric secret left after the HMAC
retirement (below).
