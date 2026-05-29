---
status: partial
---

# auth: central authority daemon + offline-verify library

**Decided.** Token authority is centralized in a single `authd`
daemon — the **sole signer**. `authd` mints every token, holds the
ES256 private key, and publishes public JWKs at `/v1/keys`. Every other
daemon **offline-verifies** tokens against cached JWKs using the `auth/`
library; no daemon mints its own tokens. Distributed / self-minting is
rejected.

This spec is **build-ready**: the schema, the claim set, the `/v1/*`
surface, the key-rotation mechanics, the TTL table, service bootstrap,
and account-linking rules below are concrete enough to implement `authd`
without further design decisions. `status: partial` reflects that the
code is not yet built, not that the design is open.

The split is two artifacts:

- **`authd`** — the daemon. Owns the ES256 private key, `auth.db`, the
  OAuth login flow, token issuance, refresh-token rotation, and JWKs
  publication. The one process that can sign.
- **`auth/`** — the library. Offline verification, scope-check,
  JWKs-cache refresh, mountable OAuth handlers, and MCP tool handlers.
  Every daemon imports it; none of them sign.

`authd` is **extracted standalone first** — the first piece of the gated
split, shipped on its own, proving the `<daemon>/api/v1/` + `types/`
pattern before `routd`/`runed`/`mcpd` follow in a later release
(sequencing: [`U-genericization.md`](U-genericization.md) "gated split").

## Why a central signer

A single signer is the load-bearing decision; everything else follows:

- **One key, one issuer.** Only `authd` holds the ES256 private key.
  Compromise surface and rotation are confined to one process; no daemon
  can forge a token because none can sign.
- **Offline verification, no hot-path hop.** Verification is a pure
  function over `(token, JWKs)`. Daemons cache `authd`'s public JWKs and
  verify in-process — no network call per request. `authd` being briefly
  down does not stop verification of already-issued tokens.
- **Centralized issuance + audit.** The single issuer is the single
  place to record issuance and to rotate the signing key (the emergency-
  revoke path, below) — impossible to colocate cleanly when every daemon
  mints its own.

Verification stays a library function so it has zero network cost;
_signing_ is the daemon's exclusive job.

## Crypto stack — mature, minimal (LOCKED)

We are an **internal token mint**, not a public OAuth/OIDC provider. We
do not implement an authorization-server framework. Three libraries, one
shared JOSE implementation:

| Library                         | Role in authd                                                                                                                                                 |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `golang.org/x/oauth2`           | Login code-exchange against Google/GitHub/Discord token endpoints (replaces hand-rolled `postForm` in `oauth.go`).                                            |
| `github.com/coreos/go-oidc/v3`  | (a) OIDC relying-party verify of Google's ID token at login; (b) `oidc.NewRemoteKeySet` for JWKs fetch + cache + rotation in **verifiers** (`auth/` library). |
| `github.com/go-jose/go-jose/v4` | ES256 **sign** in `authd`; marshal the public JWK Set served at `/v1/keys`. Rides transitively under go-oidc — one JOSE impl, shared.                         |

**Provider identity resolution** — only Google is a true OIDC provider
(returns an `id_token`); GitHub, Discord, and Telegram are not. The
resolution path per provider, ported from today's `oauth.go`:

| Provider | Flow                                                                                  | `provider_sub` source    | `name` source               | `email_verified`                 |
| -------- | ------------------------------------------------------------------------------------- | ------------------------ | --------------------------- | -------------------------------- |
| Google   | code exchange → verify `id_token` via go-oidc (`oidc.IDTokenVerifier`, nonce checked) | `id_token.sub`           | `id_token.name`             | `id_token.email_verified`        |
| GitHub   | code exchange (x/oauth2) → `GET api.github.com/user`                                  | `login` (string user id) | `name` ?? `login`           | 0 (no verified-email claim used) |
| Discord  | code exchange (x/oauth2) → `GET discord.com/api/users/@me`                            | `id`                     | `global_name` ?? `username` | 0                                |
| Telegram | Login Widget HMAC verify (no code exchange)                                           | widget `id`              | `first_name`[+`last_name`]  | 0                                |

go-oidc is used **only** for Google's `id_token` verify and for the
verifier-side `RemoteKeySet`; the other three resolve identity via their
userinfo endpoints exactly as `oauth.go` does today (the userinfo
fetchers `fetchGitHubUser`/`fetchDiscordUser`/`verifyTelegramWidget`
carry forward unchanged, only the token exchange swaps to x/oauth2).
Email-allowlist / org-membership gates (`GoogleAllowedEmails`,
`GitHubAllowedOrg`) carry forward as today.

Rejected, with reasons:

- **Hand-rolled JWT/JWKS** (today's `auth/jwt.go`): retired. HS256
  symmetric, no `kid`, no rotation, no asymmetric verify. The hand-rolled
  path is exactly the surface go-jose + go-oidc remove.
- **`ory/fosite`, `zitadel/oidc` (server side)**: full authorization-
  server frameworks (consent screens, client registration, grant-type
  state machines, token-introspection endpoints). We mint our own tokens
  for our own daemons against a fixed set of upstream IdPs; that scope is
  ~5% of what those frameworks carry. Adopting one is a net complexity
  loss.

`auth/` (verify side) never imports go-jose directly — it verifies
through `go-oidc`'s `IDTokenVerifier` / `RemoteKeySet`, which pull in
go-jose transitively. Only `authd` (sign side) imports go-jose directly,
to sign and to marshal the JWK Set.

## Revocation = short-TTL-only (LOCKED)

**No revocation-list endpoint. No revocation feed.** Verifiers stay
fully offline; they never learn per-token revocation. The trade is
explicit:

- **Normal "revoke"** = wait for natural expiry. Access tokens are
  ~15 min; the blast radius of a leaked access token is bounded by its
  TTL.
- **Revoke a refresh token** (e.g. user logout, "sign out everywhere") =
  delete its row in `refresh_tokens`. The access token it would have
  refreshed still works until its own short `exp`; no new ones issue.
  This is a server-side state change at `authd`, not a verifier concern.
- **EMERGENCY revoke** (signing key compromised, or "kill every token
  now") = **rotate the signing key**. Retire the compromised `kid`;
  every token signed by it fails verification as soon as verifiers
  refresh the JWK Set (≤ JWKS cache TTL). See § JWK rotation.

This is the deliberate reason verifiers need no revocation knowledge:
there is no per-token revocation to learn. Short TTLs + key-rotation as
the emergency lever cover every case a revocation list would.

## Sessions — short access JWT + refresh token (LOCKED)

A login produces two tokens:

1. **Access JWT** — ES256, `typ: "user"`, ~15 min TTL, verified offline
   by every daemon. Carried in `Authorization: Bearer` and (for the
   browser) `localStorage` per today's `web.go` flow.
2. **Refresh token** — opaque random 256-bit string (not a JWT),
   ~30 day TTL, **held and rotated at `authd`**, persisted as a SHA-256
   hash in `refresh_tokens`. Delivered as an `HttpOnly` cookie (today's
   `refresh_token` cookie). Lets the client silently obtain a fresh
   access JWT via `POST /v1/refresh` without re-running OAuth.

**Refresh-token rotation (one-time-use).** Every `/v1/refresh` consumes
the presented refresh token and issues a _new_ refresh token plus a new
access JWT. The consumed token's row is marked rotated (`rotated_to`
points at the successor). Presenting an already-rotated token is a reuse
signal: `authd` invalidates the entire token family (all rows reachable
via `rotated_from`/`rotated_to` from the reused token) and returns 401.
This is standard refresh-token-rotation theft detection. A missing or
expired refresh token returns 401; the client must re-login.

## Today's `auth/` (refactor base)

```
auth/
  hmac.go             generic — HMAC sign/verify of identity headers (RETIRES, see § HMAC retirement)
  jwt.go              generic — hand-rolled HS256 JWT (REPLACED by go-jose ES256)
  oauth.go            generic — hand-rolled OAuth HTTP (REPLACED by x/oauth2 + go-oidc)
  web.go              generic — login/refresh/logout/issueSession handlers (MOVE to authd, port to ES256)
  routes.go           generic — provider route registration (MOVE to authd as auth.Mount)
  link.go             generic — link-code minting (MOVE to authd)
  collide.go          generic — two-providers-one-user collision UI (MOVE to authd)
  middleware.go       generic — RequireSigned / StripUnsigned guards (KEEP, repoint at JWKs verify)
  ───
  acl.go              arizuko — folder/scope ACL evaluator (MOVE OUT to arizuko/identity.go)
  policy.go           arizuko — tier-based structural policy (MOVE OUT; tier dropped, scope-based)
  identity.go         arizuko — Identity{Folder,Tier,World} (MOVE OUT; tier dropped)
```

The refactor is concrete: the OAuth/session/link/collide handlers that
today live in `auth/` and import `core`/`store`/`theme` move **into**
`authd`, where they are re-pointed at `auth.db` and the ES256 signer;
`auth/` slims to verify-only primitives (`middleware.go` + new
`jwks.go`/`mcp.go`). `acl.go`/`policy.go`/`identity.go` move to
`arizuko/identity.go`. The genuinely-new layer is: ES256 + JWKS
(go-jose/go-oidc), the `/v1/*` API, service-token exchange, refresh-token
storage, and the `auth.db` schema.

## auth.db schema

`authd` owns `auth.db` — its own SQLite file, its own `migrations/`
subdir, per the [`U-genericization.md`](U-genericization.md) DB-ownership
rule (each daemon owns its DB + migrations; no daemon migrates another's
schema). It does **not** live in gated's `messages.db`. The existing
`auth_users` / `auth_sessions` tables in `messages.db` are the migration
source for `users` / `refresh_tokens`; the cutover (one-shot, per
[`U-genericization.md`](U-genericization.md) NO BACKWARD COMPATIBILITY)
copies their rows into `auth.db` and deletes the `messages.db` tables.

Times are RFC3339 TEXT (matches existing store convention). All `*_hash`
columns store hex SHA-256 of the secret, never the secret. Migrations
run from `authd/migrations/*.sql` at startup, same numbering convention
as `store/migrations/`.

```sql
-- users: one row per canonical account. Holds local-password creds
-- (username + argon2id hash, optional) and the display name. OAuth-only
-- users have NULL username/hash.
CREATE TABLE users (
  id          INTEGER PRIMARY KEY,
  sub         TEXT UNIQUE NOT NULL,      -- canonical subject ("u_<rand>" or first oauth sub)
  username    TEXT UNIQUE,               -- NULL for OAuth-only accounts
  hash        TEXT,                      -- argon2id encoded; NULL for OAuth-only
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL
);

-- oauth_accounts: each external identity linked to a canonical user.
-- (provider, provider_sub) is globally unique — one external identity
-- maps to at most one canonical user (§ Account linking).
CREATE TABLE oauth_accounts (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL,           -- "google" | "github" | "discord" | "telegram"
  provider_sub  TEXT NOT NULL,           -- provider's stable user id
  email         TEXT,                    -- verified email at link time, if any
  email_verified INTEGER NOT NULL DEFAULT 0,
  linked_at     TEXT NOT NULL,
  UNIQUE(provider, provider_sub)
);
CREATE INDEX idx_oauth_accounts_user ON oauth_accounts(user_id);

-- refresh_tokens: server-held, rotating, one-time-use. token_hash is
-- hex SHA-256 of the opaque token. rotated_to links a consumed token to
-- its successor (NULL = still live / leaf). family_id groups a rotation
-- chain for theft-detection invalidation.
CREATE TABLE refresh_tokens (
  id          INTEGER PRIMARY KEY,
  token_hash  TEXT UNIQUE NOT NULL,
  user_sub    TEXT NOT NULL,
  family_id   TEXT NOT NULL,             -- random; constant across one chain
  issued_at   TEXT NOT NULL,
  expires_at  TEXT NOT NULL,
  rotated_to  INTEGER REFERENCES refresh_tokens(id),  -- successor; NULL = unused
  revoked_at  TEXT                       -- set on logout / family invalidation
);
CREATE INDEX idx_refresh_user ON refresh_tokens(user_sub);
CREATE INDEX idx_refresh_family ON refresh_tokens(family_id);

-- signing_keys: ES256 keypairs. Exactly one row is "active" (signs); old
-- rows stay servable in the JWK Set until retired_at + max access TTL
-- elapses (§ JWK rotation). private_key encoding per § Private-key
-- encryption-at-rest.
CREATE TABLE signing_keys (
  kid         TEXT PRIMARY KEY,          -- "<created-unix>-<8 hex rand>"
  alg         TEXT NOT NULL DEFAULT 'ES256',
  public_pem  TEXT NOT NULL,             -- PKIX public key PEM
  private_key TEXT NOT NULL,             -- "plain:<PKCS8 PEM>" or "gcm:v1:<b64 nonce||ct>" (§ Private-key encryption-at-rest)
  active      INTEGER NOT NULL DEFAULT 0, -- 1 = current signer; only one row =1
  created_at  TEXT NOT NULL,
  retired_at  TEXT                       -- set when rotated out; row droppable after retired_at + access TTL
);

-- internal_keys: authd-internal symmetric secrets that must persist across
-- restarts + be shared by multi-instance authd via the DB (not signing keys).
-- Today's only row: 'collide_hmac' (§ Account linking + collision rules).
CREATE TABLE internal_keys (
  name        TEXT PRIMARY KEY,          -- e.g. 'collide_hmac'
  secret      TEXT NOT NULL,             -- random 256-bit; same at-rest envelope as signing_keys.private_key
  created_at  TEXT NOT NULL
);

-- service_keys: per-daemon bootstrap secret (hash) → service identity.
-- One row per daemon, seeded at compose-generate time. scope is the
-- daemon's declared service capability set (§ Service bootstrap).
CREATE TABLE service_keys (
  daemon       TEXT PRIMARY KEY,         -- "timed", "onbod", ...
  secret_hash  TEXT NOT NULL,            -- hex SHA-256 of AUTHD_SERVICE_KEY
  scope        TEXT NOT NULL,            -- JSON array of scope strings
  created_at   TEXT NOT NULL,
  rotated_at   TEXT
);

-- oauth_state: short-lived CSRF + PKCE state for the login round-trip.
-- Replaces today's signed-cookie state with a server-side row (the
-- StateStore default backing). Carries link-intent across the redirect.
CREATE TABLE oauth_state (
  state         TEXT PRIMARY KEY,        -- random; echoed in the OAuth state param
  provider      TEXT NOT NULL,
  pkce_verifier TEXT NOT NULL,
  nonce         TEXT NOT NULL,           -- OIDC id_token nonce
  link_user_sub TEXT,                    -- set when intent=link; the canonical sub to link onto
  return_to     TEXT,                    -- validated relative path
  created_at    TEXT NOT NULL,
  expires_at    TEXT NOT NULL
);
CREATE INDEX idx_oauth_state_expiry ON oauth_state(expires_at);
```

Expired `oauth_state`, `refresh_tokens`, and `retired_at`-elapsed
`signing_keys` rows are swept by a periodic GC goroutine (cadence:
hourly). No external dependency.

### Private-key encryption-at-rest

`signing_keys.private_key` is a tagged string, so plaintext and
encrypted rows are self-describing (no per-row flag column):

- **`AUTHD_KEY_ENCRYPTION_KEY` unset** (single-host default): stored as
  `"plain:" + <PKCS8 PEM>`. The trade is explicit — the DB file is the
  trust boundary; an attacker with the file already owns the host.
- **set** (32-byte key, hex- or base64-encoded): AES-256-GCM. Stored as
  `"gcm:v1:" + base64( nonce(12 bytes) || ciphertext || tag(16 bytes) )`
  — the standard Go `cipher.AEAD.Seal(nonce, nonce, plaintext, nil)`
  layout with the 12-byte nonce prepended. `v1` is the envelope version
  for forward-compat. The plaintext is the PKCS8 PEM; AAD is the `kid`
  (binds ciphertext to its row). Reader dispatches on the `plain:` /
  `gcm:v1:` prefix.

### Migrating from messages.db

The cutover (step 2) copies the existing `auth_users` and `auth_sessions`
rows out of gated's `messages.db` into `auth.db`, then drops the source
tables (one-shot, [`U-genericization.md`](U-genericization.md) NO
BACKWARD COMPATIBILITY). Source schemas (today, `store/migrations/0001`

- `0040`):

```sql
auth_users(id, sub UNIQUE, username UNIQUE, hash, name, created_at, linked_to_sub)
auth_sessions(token_hash PRIMARY KEY, user_sub, expires_at, created_at)
```

Field mapping:

| Source (`messages.db`)                            | Target (`auth.db`)                                                                         | Rule                                                                                                       |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------- |
| `auth_users` (canonical: `linked_to_sub IS NULL`) | `users(sub, username, hash, name, created_at)`                                             | one `users` row per canonical sub; carry `username`/`hash` for local-password users (NULL for OAuth-only). |
| `auth_users` (linked: `linked_to_sub = X`)        | `oauth_accounts(user_id, provider, provider_sub)`                                          | split each `sub` of form `"<provider>:<id>"` → `(provider, id)`; `user_id` = the row whose `sub` = `X`.    |
| `auth_users` (canonical, OAuth sub `"<p>:<id>"`)  | also an `oauth_accounts` row                                                               | a canonical OAuth user is its own first linked account: insert `(user_id, provider, id)` for its own sub.  |
| `auth_sessions(token_hash, user_sub, expires_at)` | `refresh_tokens(token_hash, user_sub, family_id=random, issued_at=created_at, expires_at)` | one family per migrated session; `rotated_to=NULL`. Local-password `hash` users keep working unchanged.    |

`signing_keys` and `service_keys` have no source rows — generated fresh
(first-boot keypair; compose-seeded service keys).

**Service-key seed mechanism.** Compose generation writes the per-daemon
hashes into a seed authd reads at boot: `AUTHD_SERVICE_KEYS_FILE` points
at a JSON map `{ "<daemon>": { "secret_hash": "<hex>", "scope": [...] } }`
(written next to the other generated config). On startup authd UPSERTs
each entry into `service_keys` by `daemon` (insert, or update
`secret_hash`/`scope` if the row exists) — idempotent, so rotation = compose
re-generates the file with new hashes and authd re-seeds on next restart.
No admin API; the seed file is the single provisioning path. A `service_keys`
row absent from the seed is left untouched (manual rows survive).

## JWT claim set

Every minted token is an ES256 JWS with header `{"alg":"ES256","typ":"JWT","kid":"<active kid>"}`.
`kid` is **required** in the header (verifiers select the public key by
it). The arizuko folder is namespaced under a private claim to keep
`auth/` domain-agnostic.

| Claim        | Type       | Semantics                                                         | user | service | downscoped |
| ------------ | ---------- | ----------------------------------------------------------------- | ---- | ------- | ---------- |
| `iss`        | string     | Literal `"authd"`. Verifiers pin this exact value.                | req  | req     | req        |
| `sub`        | string     | `user:<canonical-sub>` / `service:<daemon>` / inherits parent     | req  | req     | req        |
| `aud`        | string     | Target audience; `""`/omitted = any (single-deployment default)   | opt  | opt     | opt        |
| `iat`        | int (unix) | Issued-at                                                         | req  | req     | req        |
| `nbf`        | int (unix) | Not-before (= `iat`)                                              | req  | req     | req        |
| `exp`        | int (unix) | Expiry (`iat` + TTL per § TTL table)                              | req  | req     | req        |
| `jti`        | string     | Unique token id (random); for audit correlation                   | req  | req     | req        |
| `typ`        | string     | `"user"` \| `"service"` \| `"downscoped"` (claim, not JWS header) | req  | req     | req        |
| `scope`      | []string   | Capability list (`["tasks:write","messages:send"]`)               | req  | req     | req        |
| `parent_jti` | string     | `jti` of the token this was downscoped from                       | —    | —       | req        |
| `arz/folder` | string     | arizuko folder subtree this token is scoped to                    | opt  | opt     | opt        |

- `typ` is a **claim**, distinct from the JWS header `typ:"JWT"`. It
  drives verifier-side policy (e.g. a `downscoped` token must carry
  `parent_jti`).
- `scope` is namespace-wildcard-capable (`tasks:*`) but **never** the
  global `*:*` ([`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)
  § Scope vocabulary). Match logic lives in `auth.HasScope`.
- `arz/folder` is the namespaced folder claim; `auth/` treats it as
  opaque and exposes it via `Identity.Extra["folder"]`. The arizuko
  helper in `arizuko/identity.go` reads it. Root/operator tokens omit it.
- A clock-skew tolerance of 30s applies to `nbf`/`iat`/`exp` (matches
  today's `clockSkew`).

`Claims` (mint input) and `Identity` (verify output) carry these as
generic fields:

```go
type Claims struct {
    Sub        string            // "user:..." | "service:..." | inherited
    Typ        string            // "user" | "service" | "downscoped"
    Scope      []string          // capability list
    Audience   string            // optional
    ParentJTI  string            // downscoped only
    Extra      map[string]string // app-specific; "folder" → arz/folder claim
    TTL        time.Duration
}

type Identity struct {
    Sub       string
    Typ       string
    Scope     []string
    Audience  string
    ParentJTI string
    Extra     map[string]string // includes "folder" for arizuko
    Issuer    string            // "authd"
    JTI       string
    Expires   time.Time
}
```

There is no `tier` field — scopes replace tier everywhere
([`U-genericization.md`](U-genericization.md) "Capability-vs-tier").

## `/v1/*` API surface

**Route-naming decision (resolved).** Machine token + key endpoints live
under `/v1/*`; the human OAuth browser flow keeps the established
`/auth/*` prefix (proxyd already 302s to `authd_url/auth/login` —
[`35-proxyd-standalone.md`](35-proxyd-standalone.md) § Login flow). Two
prefixes, two audiences: `/v1/*` is the typed API contract
(`authd/api/v1/`); `/auth/*` is the browser-redirect surface. They do not
overlap.

All `/v1/*` JSON errors use the shape `{"error":"<code>","message":"<human>"}`
with the HTTP status carrying the class. `GET /v1/keys` is **public**
(mounted before any auth middleware); every other `/v1/*` endpoint
requires either a bootstrap secret (`/v1/service-token`) or a bearer
token (mint/downscope).

### `GET /v1/keys` — JWK Set (public)

The JWK Set verifiers cache. Marshalled by go-jose from `signing_keys`
rows that are `active` OR not-yet-fully-retired (overlap window).

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

Cache headers: `Cache-Control: public, max-age=3600` (matches the JWKS
cache TTL). Verifiers also refresh on `kid`-miss (a token whose `kid`
isn't in the cached set forces one re-fetch before failing) — this is
`go-oidc`'s `RemoteKeySet` behavior, used as-is.

### `POST /v1/tokens` — mint (bearer or service required)

Mints a token. Two modes, distinguished by the caller's scope:

- **Issuer mint** (caller bearer carries `tokens:mint`, e.g. onbod,
  dashd, proxyd-on-login): mints a fresh `user`/`service` token for a
  **different** `sub` than the caller, with the requested `scope`/`folder`.
  The cap is the caller's own scope: the requested `scope` MUST be a
  subset of the caller's `scope` (so a `tokens:mint` holder cannot mint
  scopes it doesn't itself hold) and `requested folder` MUST be within
  the caller's `arz/folder` subtree. `typ` may be `user` or `service`
  (the trigger declares it; an invite mints `user`, never `service`).
  This is the privilege-creep guard: `tokens:mint` lets you _delegate_
  your authority to another subject, never _escalate_ — the minted token
  is always ⊆ the minter. Violations → `403 scope_exceeds_minter`.
- **Downscope** (caller bearer is any valid token, no `tokens:mint`
  needed): mints a `downscoped` token for the **same** `sub` as the
  caller (the `sub` field is ignored / forced to the caller's), whose
  `scope` MUST be ⊆ the caller's own scope and whose `arz/folder` MUST be
  within the caller's folder subtree; `parent_jti` is set to the caller's
  `jti`. Broader-than-parent → `403 scope_exceeds_parent`. TTL is capped
  at the parent's remaining lifetime.

One endpoint; the server picks mode by whether the caller holds
`tokens:mint` AND requested `sub` ≠ caller `sub` (issuer mint) vs same
`sub` (downscope). No separate `/v1/downscope`. In both modes the
subset/subtree check is the only authority rule — there is no path by
which the response token exceeds the request's bearer.

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

A daemon exchanges its bootstrap secret for a short-lived service JWT.
The bootstrap secret goes in the `Authorization` header as
`Bearer <AUTHD_SERVICE_KEY>` (not the body — keeps it out of logs that
record bodies). The daemon identity comes from the body. `authd` looks
up `service_keys` by `daemon`, compares the secret hash in constant time,
and signs a `service` token with that row's `scope`.

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

Consumes a refresh token, rotates it (§ Sessions), returns a new access
JWT, and returns the successor refresh token by the same channel it was
presented on:

- **Browser** (token came from the `refresh_token` cookie): the successor
  is written back as the `Set-Cookie: refresh_token=...` header and is
  **omitted** from the JSON body (it stays `HttpOnly`, never visible to
  JS).
- **Non-browser** (token came from the JSON body `{"refresh_token":...}`,
  no cookie): the successor is returned in the JSON body as
  `refresh_token`; no `Set-Cookie` is written. The client persists it and
  presents it on the next refresh.

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

Bearer with scope `keys:rotate`. Generates a new ES256 key, makes it
`active`, retires the old one (§ JWK rotation). The HTTP equivalent of
`authd rotate-key`.

```jsonc
// POST /v1/keys/rotate  Authorization: Bearer <operator>
{ "revoke_old": false }   // true = emergency revoke: zero overlap, old kid dropped from JWK Set now
// 200
{ "new_kid":"1735003600-9f8e7d6c", "retired_kid":"1735000000-a1b2c3d4",
  "old_servable_until":"2026-06-28T12:00:00Z" }  // null when revoke_old=true
// 403 {"error":"forbidden","message":"keys:rotate required"}
```

### `DELETE /v1/users/me/accounts/{provider}` — unlink an OAuth account

Bearer = the user's own access JWT (scope: self — the `sub` in the token
owns the `users` row). Removes the matching `oauth_accounts` row.

```jsonc
// DELETE /v1/users/me/accounts/google   Authorization: Bearer <user>
// 204  (no body)
// 404 {"error":"not_linked","message":"no google account on this user"}
// 409 {"error":"last_login_method","message":"cannot unlink the only login method"}
```

`{provider}` ∈ `{google,github,discord,telegram}`. A user has at most one
account per provider (`(provider, provider_sub)` unique + one user owns at
most one sub per provider in practice), so the path needs no
`provider_sub`. Refused with `409` when removing it would leave the user
with no `oauth_accounts` row and no local-password (`users.hash IS NULL`).

### OAuth / login routes (browser, `/auth/*`)

Ported from today's `web.go`/`oauth.go`/`routes.go` into `authd`, minting
ES256 instead of HS256. `authd` is where the session JWT is minted
because only the signer can issue one.

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

- `<provider>` ∈ `{google, github, discord}`, mounted only when its
  client-id config is set (today's conditional registration in
  `routes.go`).
- `issueSession` (ported from `web.go`): canonicalize sub, snapshot
  scopes from grants (§ Login-time scope snapshot), mint the access JWT,
  create a `refresh_tokens` row, set the `HttpOnly` cookie, return the
  access JWT to the browser via the `localStorage` bootstrap HTML
  (today's `web.go` pattern, unchanged).
- **Response per route (content negotiation by `Accept`).** Browser flows
  — `GET /auth/<provider>/login` (302 to the IdP), its `…/callback`, and a
  `POST /auth/login` from a browser (`Accept: text/html`) — complete by
  returning the `localStorage` bootstrap HTML (today's `web.go` pattern)
  that stashes the access JWT then redirects to the validated `return`.
  Programmatic callers (`Accept: application/json`) get
  `200 {"token":"<jws>","expires_at":...,"refresh_token":"<opaque>"}` and no
  HTML — the **initial** refresh token rides the JSON body for non-browser
  clients (they have no cookie jar), exactly as `/v1/refresh` returns the
  rotated successor. The refresh token is delivered as an `HttpOnly` cookie
  (browser) **or** the JSON body (non-browser) — never both, on both initial
  login and refresh. `/auth/me` reads the access JWT bearer, never the cookie.
- `return` is validated as a relative path (today's `safeReturn`); rate
  limiting on `POST /auth/login` per-IP (today's `loginLimiter`,
  5/15 min) carries forward.

### Login-time scope snapshot

`authd` does not own grants — gated does (the grants owner, today's
`store.UserScopes`; [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)
§ scope minter). At session issuance `authd` fetches the caller's scope
snapshot over a single HTTP call to the grants owner:

```
GET <GRANTS_URL>/v1/users/{sub}/scopes      Authorization: Bearer <authd service token>
→ 200 {"scope":["messages:send","tasks:read","groups:read:own_group"], "folder":"atlas/main"}
→ 404 {"error":"no_grants"}   (sub has no grant rows)
```

- `authd` authenticates this call with its own `service:authd` token
  (scope `grants:read`), obtained at boot from its own signer.
- The returned `scope` + `folder` are stamped into the access JWT as
  `scope` and `arz/folder`. The snapshot is taken **once at issuance**;
  later grant changes do not retroactively alter a live token (they take
  effect at the next refresh / login — consistent with the short-TTL
  revocation model).
- **Failure / default**: `404 no_grants` → mint a session with **empty**
  `scope` and **no** `arz/folder` (the user is authenticated but
  unauthorized for any resource; the browser is redirected to `/onboard`,
  today's `web.go` `dest = "/onboard"` for zero-group users). A 5xx from
  the grants owner → `503 grants_unavailable`, login fails closed (no
  token minted) rather than minting an empty-scope session that masks the
  outage.
- `POST /v1/refresh` re-runs this snapshot so a refreshed token reflects
  current grants.

`GRANTS_URL` defaults to gated; a standalone `authd` deployment without
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
  key `active=0` with `retired_at = now`. Both public keys stay in
  `GET /v1/keys` until `retired_at + max(access TTL, JWKS cache TTL)`
  elapses, so already-issued tokens signed by the old `kid` keep
  verifying until they would have expired anyway. The GC sweep drops the
  row after that.
- **Retired-key retention**: a retired key's public half is servable for
  the overlap window only; its private half can be zeroed at retirement
  (we never re-sign with it).
- **Compromise procedure = emergency revoke** (§ Revocation): operator
  runs `authd rotate-key --revoke-old` which rotates AND sets the
  compromised key's `retired_at` to `now` with **zero** overlap (drops it
  from the JWK Set immediately). Every token signed by it fails
  verification within one JWKS cache TTL (≤1 h; force-purge by restarting
  verifiers or shortening the cache TTL during an incident). This is the
  single lever that invalidates everything at once.

## TTL table

Defaults; the `AUTHD_*` overrides make each configurable. Values chosen
to keep the short-TTL-revocation model honest.

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

Daemon-initiated work (`timed` firing a task, `onbod` admitting from the
queue, a cron sweep) has no user in the loop but still needs an identity
to call gated/router. That identity is a **service identity**: an
`Identity` whose `sub` is `service:<daemon>` and whose scope is the
daemon's capability grant. No second verification path, no second trust
boundary — a service token verifies exactly like a user token.

- **Who generates the secret**: **compose generation**
  (`compose/compose.go`) writes one random `AUTHD_SERVICE_KEY` per daemon
  into that daemon's compose env, and seeds `authd`'s `service_keys` table
  with `(daemon, sha256(secret), scope)` at the same time. The secret is
  never shared between daemons; `authd` stores only its hash.
- **Capability source of truth**: each daemon's service scope set is
  declared once, in its `template/services/<daemon>.toml` (a
  `service_scope = ["messages:write","tasks:read"]` key), aggregated by
  `compose.go` into the `service_keys` seed. This keeps scope declaration
  next to the rest of the daemon's compose env (CLAUDE.md § adding a
  channel adapter — no edit to `compose.go` for a new daemon's scope).
- **Daemon→secret binding**: the exchange (`POST /v1/service-token`) is
  authenticated by the secret (in the `Authorization` header) and
  identified by `daemon` (in the body); `authd` binds them via the
  `service_keys` row. A leaked `AUTHD_SERVICE_KEY` buys an attacker
  exactly that **one** daemon's scoped service token — never the ability
  to sign arbitrary tokens, because only `authd` holds the private key.
- **Rotation**: reissue the env secret (compose re-generate updates both
  the daemon env and the `service_keys` hash) and restart the daemon.
  `service_keys.rotated_at` records it.

```go
// auth/ library, inside a daemon. Exchanges the bootstrap secret for a
// short-lived service JWT and keeps it refreshed; the daemon presents
// the returned token on daemon→daemon calls; it never holds a signing key.
func ServiceToken(authdURL, daemon, bootstrapKey string) (*TokenSource, error)
```

Bootstrap secrets are the **only** symmetric secret left after the HMAC
retirement (below).

## Account linking + collision rules

One canonical `users` row may have multiple `oauth_accounts` (Google +
GitHub + …). Rules, ported and made explicit from today's
`oauth.go`/`collide.go`:

- **Uniqueness**: `(provider, provider_sub)` is globally unique. One
  external identity → at most one canonical user.
- **First login**: no matching `oauth_accounts` row and no current
  session → create a `users` row (`sub = "u_<rand>"`, `name` from the
  provider), insert the `oauth_accounts` row, issue a session.
- **Returning login**: `(provider, provider_sub)` matches an
  `oauth_accounts` row → resolve to its `user_id`, issue a session for
  that user's canonical `sub`.
- **Explicit link** (`intent=link`, carried through the round-trip in the
  `oauth_state.link_user_sub` column, set from the current session at
  `/auth/<provider>?intent=link`):
  - new external identity (no existing `oauth_accounts` row) → insert the
    row pointing at the current user. Linked.
  - external identity **already linked to a different user** → **hard
    fail** with the collision screen (`collide.go`). We do **not** merge
    two existing canonical users automatically; the "Link" button is
    disabled when both sides already exist (today's `collide.go`
    `NewCanonical` disabled state). Merging two populated accounts is an
    operator action, out of scope for the self-serve flow.
- **Implicit collision** (no `intent=link`, but a session exists and the
  just-authenticated identity maps elsewhere or nowhere) → show the
  collision screen offering: link the new identity to the current account
  (only if it's unlinked), or log out and continue as the other account.
- **Auto-link-by-verified-email**: **NO.** We do not auto-merge accounts
  by matching verified email — that is a known account-takeover vector if
  one provider's email verification is weaker. Linking is always explicit
  (`intent=link`) or first-login. `email`/`email_verified` are recorded
  on `oauth_accounts` for audit and for the email-allowlist gate
  (today's Google `GoogleAllowedEmails` / GitHub org check), not for
  silent linking.
- **Collision-form key.** The `collide.go` form carries a short-lived
  signed `collideToken` (the pending decision between login round-trips).
  Its HMAC key is a persistent authd-internal secret: row
  `name='collide_hmac'` in `internal_keys` (§ schema), generated first-boot.
  Persisting it in `auth.db` (not process memory) is what lets an in-flight
  collision form survive an authd restart and stay valid across multiple
  authd instances sharing the DB. ~10 min token TTL.
- **Unlink**: `DELETE /v1/users/me/accounts/{provider}` (§ `/v1/*` API
  surface) removes an `oauth_accounts` row; refused with `409` if it is
  the user's **last** login method and they have no local password.

The collision screen and its signed `collideToken` (HMAC over a short-TTL
payload, today's `collide.go`) port unchanged except the HMAC secret
becomes an `authd`-internal key (it never leaves `authd`, so it stays
symmetric — it is a CSRF token, not an identity token).

## Target shape — four library/daemon surfaces

A single Go module exposing four surfaces. `auth/` is verify-only;
`authd` is the daemon.

### 1. Verification primitives (library — every daemon)

```go
// Verify any incoming token against cached JWKs. Pure function once the
// JWKs cache is warm. ES256: picks the public key by `kid` from the JWS
// header (go-oidc RemoteKeySet). Pins iss=="authd", checks exp/nbf/aud.
func VerifyHTTP(r *http.Request, jwks *KeySet) (Identity, error)
func VerifyToken(token string, jwks *KeySet) (Identity, error)

// Scope check. Authorization is scope-match; there is no tier.
func HasScope(ident Identity, resource, verb string) bool   // honors "ns:*"; never "*:*"
func MatchesAudience(ident Identity, aud string) bool

// JWKs cache: wraps oidc.NewRemoteKeySet against authd's /v1/keys.
// Refreshes on `kid` miss or TTL. Never needs the private key.
type KeySet struct{ /* go-oidc RemoteKeySet + iss/aud config */ }
func FetchKeys(authdURL string) (*KeySet, error)
```

### 2. Mint primitives (authd only — the sole signer)

Minting lives **only** in `authd`, which holds the ES256 private key. No
other daemon links a signing path:

```go
// Inside authd. Signs with the active ES256 key (go-jose), stamps the
// active kid into the JWS header.
func (a *Authd) Mint(claims Claims) (string, error)

// Downscope: mint a narrower token from an existing one. Backs the
// `mint_token` MCP tool (which forwards to authd). Errors if requested
// scope ⊄ parent scope or folder ⊄ parent folder.
func (a *Authd) MintNarrower(parent Identity, claims Claims) (string, error)
```

### 3. OAuth flow handlers (mountable — authd mounts them)

```go
type Provider struct {
    ID, Type, ClientID, ClientSecret, IssuerURL string  // IssuerURL → go-oidc discovery
    Scopes []string
}

type AuthHandlers struct {
    Login, Callback, Logout, Me http.HandlerFunc // GET/POST per § OAuth routes
}
func Handlers(providers []Provider, signer Signer, opts ...Option) AuthHandlers
func Mount(mux *http.ServeMux, providers []Provider, signer Signer, opts ...Option)
```

`authd` mounts these; proxyd delegates login to `authd`
([`35-proxyd-standalone.md`](35-proxyd-standalone.md) § Login flow) — it
enforces, it does not sign. `StateStore` (default: `oauth_state` table)
and `LinkStore` (default: `auth.db` `oauth_accounts`/`users`) are
pluggable interfaces; daemons that don't need linking pass nil.

### 4. MCP tool handlers (mountable)

```go
// Read-only tools (whoami, verify_token, list_providers) run in-process
// against cached JWKs. mint_token forwards to authd over HTTP.
func MCPTools(authdURL string, jwks *KeySet) []MCPTool
```

| Tool             | Purpose                                        | Scope required            |
| ---------------- | ---------------------------------------------- | ------------------------- |
| `whoami`         | Return the caller's own Identity               | none                      |
| `mint_token`     | Mint a token narrower than caller's own scope  | (downscope-only enforced) |
| `verify_token`   | Introspect a token (does it parse, what scope) | any valid token           |
| `list_providers` | List configured OAuth providers                | none                      |

`mint_token` forwards to `authd /v1/tokens` (downscope mode); the host
never signs. This is the primitive that makes agent → sub-agent
delegation safe without admin in the loop: the agent holds its own token,
calls `mint_token`, `authd` uses the agent's scope as the parent, signs
the narrower token, and returns it to the agent to hand to a sub-agent.

The MCP host (gated's ipc subsystem today; `mcpd` after the split) mounts
these alongside its existing tools.

## Mounting pattern (a verifying daemon)

```go
import "github.com/kronael/arizuko/auth"

func main() {
    jwks, _ := auth.FetchKeys(os.Getenv("AUTHD_URL")) // public keys only
    svc, _  := auth.ServiceToken(os.Getenv("AUTHD_URL"), "timed",
                                 os.Getenv("AUTHD_SERVICE_KEY"))

    mux := http.NewServeMux()
    mux.Handle("/v1/", auth.RequireSigned(jwks)(handler))   // verify offline

    for _, tool := range auth.MCPTools(os.Getenv("AUTHD_URL"), jwks) {
        mcpServer.RegisterTool(tool)                        // mint_token forwards to authd
    }
    // ... daemon's own routes; daemon→daemon calls carry svc.Token() ...
    http.ListenAndServe(":8080", mux)
}
```

Only `authd` holds the private key and mounts `auth.Mount` (the OAuth +
mint surface). Every other daemon carries public JWKs and verifies
offline.

## HMAC retirement plan

The authd cutover (a [`U-genericization.md`](U-genericization.md)
genericization milestone) retires the two remaining shared symmetric
secrets. One-shot, no dual-secret period.

| Secret today                                                     | Used for                                                                   | Replaced by                                                                                                                                             |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PROXYD_HMAC_SECRET` / `AUTH_SECRET` (`hmac.go` `VerifyUserSig`) | proxyd signing identity headers (`X-User-Sub`/`-Sig`) that backends verify | ES256 bearer token: proxyd verifies the authd-minted JWT and forwards `Authorization: Bearer`; backends verify the same JWT offline. `hmac.go` deletes. |
| `CHANNEL_SECRET` (`hmac.go` `VerifyChatSig`)                     | channel adapters signing chat-token headers (`X-Chat-Token`/`-Sig`)        | service token: each adapter exchanges `AUTHD_SERVICE_KEY` for a `service:<adapter>` JWT and presents it; gated verifies offline.                        |

After the cutover the only symmetric secrets left are the per-daemon
`AUTHD_SERVICE_KEY` bootstrap secrets (§ Service bootstrap) and the
`authd`-internal collision-CSRF key (never leaves `authd`). The
signed-identity-header path (`auth/middleware.go` `RequireSigned` /
`StripUnsigned`) is re-pointed from HMAC-header verification to
JWKs-bearer verification in the same cutover.

## Where each role lives, after this lands

| Role                                  | Where                                                                                    |
| ------------------------------------- | ---------------------------------------------------------------------------------------- |
| Verify a token                        | Any daemon — `auth.VerifyHTTP` against cached JWKs, no hop                               |
| Mint a token from claims              | `authd` only — the sole signer                                                           |
| Downscope an existing token           | `authd.MintNarrower` (MCP `mint_token` / `POST /v1/tokens` downscope mode forward to it) |
| Publish public JWKs                   | `authd` at `GET /v1/keys` (public)                                                       |
| OAuth login + callback                | `authd` (`/auth/*`); proxyd delegates                                                    |
| Silent token refresh                  | `authd` at `POST /v1/refresh` (rotating refresh tokens)                                  |
| Service token exchange                | `authd` at `POST /v1/service-token`; `auth.ServiceToken` refreshes it                    |
| Key rotation / emergency revoke       | `authd` (`authd rotate-key`, `POST /v1/keys/rotate`)                                     |
| MCP tools (`whoami`, `mint_token`, …) | gated's ipc subsystem / `mcpd` (mounts `auth.MCPTools`)                                  |
| Account-linking + refresh storage     | `auth.db` (`oauth_accounts`/`users`/`refresh_tokens`) — owned by `authd`                 |

## What this spec is not

- Not distributed minting. `authd` is the sole signer; backends only
  verify.
- Not symmetric token crypto. ES256 from launch; daemons hold only public
  JWKs. (Bootstrap secrets are exchange credentials, not token signers.)
- Not a public OAuth/OIDC authorization server. We are an internal mint
  (§ Crypto stack) — no client registration, consent screens, or
  token-introspection endpoint.
- Not a per-token revocation list or feed (§ Revocation). Short TTLs +
  key rotation are the levers.
- Not full OIDC conformance for the **issuer** side. We are an OIDC
  _relying party_ (go-oidc verifies upstream IdP tokens at login); our
  own issued tokens are plain ES256 JWTs.

## Implementation phases (build order; one-shot cutover per U-genericization)

`authd` is extracted **standalone first**, before the rest of the gated
split. Each step is committed + verified; the final cutover is one
release ([`U-genericization.md`](U-genericization.md) NO BACKWARD
COMPATIBILITY).

1. **Extract arizuko-specific code.** Move `acl.go`, `policy.go`,
   `identity.go` out of `auth/` into `arizuko/identity.go` (folder
   helpers reading `Identity.Extra["folder"]`; tier dropped). `auth/`
   left with generic primitives only. Pure refactor.
2. **Stand up `authd` + `auth.db`.** New daemon; `auth.db` schema +
   migrations (§ auth.db schema). Generate ES256 keypair on first boot;
   serve `GET /v1/keys` (go-jose JWK Set). Implement `Mint` +
   `MintNarrower`, `POST /v1/tokens`. Migrate the `auth_users` /
   `auth_sessions` rows out of `messages.db`.
3. **Port OAuth + session handlers into `authd`.** Move
   `web.go`/`oauth.go`/`routes.go`/`link.go`/`collide.go` into `authd`,
   re-point at `auth.db` + the ES256 signer; swap hand-rolled OAuth for
   x/oauth2 + go-oidc. Implement `/v1/refresh` (rotating refresh tokens),
   `/v1/service-token`, `/auth/me`.
4. **Offline verification everywhere.** `auth/jwks.go` wraps
   `oidc.NewRemoteKeySet`; `RequireSigned` re-points from HMAC headers to
   JWKs-bearer. Backends call `auth.FetchKeys` + `auth.ServiceToken`. MCP
   `mint_token` forwards to `authd`. HMAC retirement (delete `hmac.go`,
   drop `PROXYD_HMAC_SECRET`/`CHANNEL_SECRET`).
5. **Document for non-arizuko deployment.** `authd/README.md` +
   `auth/README.md`: standalone `authd`, "verify with the library",
   provider config (Google/GitHub/Discord/OIDC), account linking, MCP
   integration, `authd/api/v1/` types + client.

After step 2 `authd` signs and JWKs are live. After step 4 every backend
verifies offline and HMAC is gone. The rest of the gated split follows in
a later release.

## Code pointers

- `auth/middleware.go` — `RequireSigned`/`StripUnsigned`; re-point from
  HMAC headers to JWKs-bearer verify.
- `auth/jwks.go` (new) — `KeySet`, `FetchKeys`, ES256 verify-by-`kid`
  (wraps go-oidc `RemoteKeySet`).
- `auth/mcp.go` (new) — `MCPTools`.
- `auth/service.go` (new) — `ServiceToken` / `TokenSource`.
- `auth/hmac.go`, `auth/jwt.go`, `auth/oauth.go`, `auth/web.go`,
  `auth/routes.go`, `auth/link.go`, `auth/collide.go` — `hmac.go`/`jwt.go`
  **delete**; the OAuth/session/link/collide files **move into `authd`**
  and port to ES256 + x/oauth2 + go-oidc.
- `auth/acl.go`, `auth/policy.go`, `auth/identity.go` — move to
  `arizuko/identity.go` (tier dropped, scope-based).
- `authd/` (new daemon) — ES256 private key, `auth.db` + `migrations/`,
  mounts `auth.Mount` + `/v1/*`. The sole signer.
- `authd/api/v1/` (new) — wire types + thin client (the first instance of
  the [`U-genericization.md`](U-genericization.md) `<daemon>/api/v1/`
  pattern).
- `compose/compose.go` — seed `service_keys` + write per-daemon
  `AUTHD_SERVICE_KEY`; aggregate `service_scope` from
  `template/services/*.toml`.
- `proxyd/main.go` — delegates login to `authd`; verifies via
  `auth.FetchKeys`; no local mint; HMAC header path deleted.
- `gated/ipc/...` (or `mcpd` after the split) — registers
  `auth.MCPTools`; `mint_token` forwards to `authd`.

## Blueprint takeaway

The pattern this spec lands on — **central authority, distributed
verification**:

1. The thing that **signs** is a single daemon (`authd`). One private
   key, one issuer, one place to rotate (the emergency-revoke lever) and
   audit. Anything that mints trust is a singleton.
2. The thing that **verifies** is a library every daemon imports.
   Verification is a pure function over `(token, public JWKs)` — no
   network hop, no fault dependency on `authd` being up.
3. `authd` publishes its public keys (JWKs at `/v1/keys`); consumers
   cache them and verify offline; `kid` drives rotation; short TTLs +
   key-rotation cover revocation without a per-token list.

This is the first piece of the gated split and the blueprint for the
`<daemon>/api/v1/` + `types/` pattern the rest of the split adopts later
([`U-genericization.md`](U-genericization.md) "gated split").
