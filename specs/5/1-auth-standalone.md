---
status: partial
---

# auth: central authority daemon + offline-verify library

**DECISION.** Token authority is centralized in a single `authd` daemon —
the **sole signer**. `authd` mints every token, holds the ES256 private key,
and publishes public JWKs at `/v1/keys`. Every other daemon
**offline-verifies** against cached JWKs via the `auth/` library; no daemon
mints its own. Distributed / self-minting is rejected. Verification is a pure
function over `(token, JWKs)` — no per-request hop, and `authd` being briefly
down doesn't stop verification of already-issued tokens. The single issuer is
the one place to record issuance and rotate the signing key (the
emergency-revoke lever, below).

This spec is **build-ready** (`status: partial` = not yet built, not open
design). Two artifacts:

- **`authd`** — the daemon. Owns the ES256 private key, `auth.db`, the OAuth
  login flow, token issuance, refresh-token rotation, JWKs publication. The
  one process that can sign.
- **`auth/`** — the library. Offline verification, scope-check, JWKs-cache
  refresh, mountable OAuth handlers, MCP tool handlers. Every daemon imports
  it; none sign.

`authd` is **extracted standalone first** — the first piece of the gated
split, proving the `<daemon>/api/v1/` + `types/` pattern before
`routd`/`runed`/`mcpd` follow ([`U-genericization.md`](U-genericization.md)
"gated split").

## Crypto stack — mature, minimal (LOCKED)

We are an **internal token mint**, not a public OAuth/OIDC authorization
server. Three libraries, one shared JOSE implementation:

| Library                         | Role in authd                                                                                                                                                 |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `golang.org/x/oauth2`           | Login code-exchange against Google/GitHub/Discord token endpoints (replaces hand-rolled `postForm` in `oauth.go`).                                            |
| `github.com/coreos/go-oidc/v3`  | (a) OIDC relying-party verify of Google's ID token at login; (b) `oidc.NewRemoteKeySet` for JWKs fetch + cache + rotation in **verifiers** (`auth/` library). |
| `github.com/go-jose/go-jose/v4` | ES256 **sign** in `authd`; marshal the public JWK Set served at `/v1/keys`. Rides transitively under go-oidc — one JOSE impl, shared.                         |

**Provider identity resolution** — only Google is a true OIDC provider
(returns an `id_token`); GitHub, Discord, and Telegram are not. The
resolution path per provider, ported from today's `oauth.go`:

| Provider | Flow                                                                                  | `provider_sub` source  | `name` source               | `email_verified`                 |
| -------- | ------------------------------------------------------------------------------------- | ---------------------- | --------------------------- | -------------------------------- |
| Google   | code exchange → verify `id_token` via go-oidc (`oidc.IDTokenVerifier`, nonce checked) | `id_token.sub`         | `id_token.name`             | `id_token.email_verified`        |
| GitHub   | code exchange (x/oauth2) → `GET api.github.com/user`                                  | `id` (numeric, stable) | `name` ?? `login`           | 0 (no verified-email claim used) |
| Discord  | code exchange (x/oauth2) → `GET discord.com/api/users/@me`                            | `id`                   | `global_name` ?? `username` | 0                                |
| Telegram | Login Widget HMAC verify (no code exchange)                                           | widget `id`            | `first_name`[+`last_name`]  | 0                                |

go-oidc is used **only** for Google's `id_token` verify and the
verifier-side `RemoteKeySet`; the other three resolve identity via userinfo
endpoints as today (`fetchGitHubUser`/`fetchDiscordUser`/`verifyTelegramWidget`
carry forward unchanged, only the token exchange swaps to x/oauth2).
Email-allowlist / org gates (`GoogleAllowedEmails`, `GitHubAllowedOrg`) carry
forward.

**Rejected:** hand-rolled JWT/JWKS (today's `auth/jwt.go`, HS256, no `kid`/
rotation/asymmetric verify) — exactly what go-jose + go-oidc remove; and
`ory/fosite` / `zitadel/oidc` — full authorization-server frameworks (consent,
client registration, grant-type state machines, introspection) that are ~20×
our scope.

`auth/` (verify side) never imports go-jose directly. For arizuko-issued
ES256 tokens it uses go-oidc's `RemoteKeySet` to fetch/cache/rotate the JWK
Set, then runs a **plain JWT verify** (parse, select key by `kid`, check
signature + `iss`/`exp`/`nbf`) — **not** `IDTokenVerifier`, which is
OIDC-`id_token`-only. `IDTokenVerifier` is used **solely** at login to verify
Google's `id_token` (go-jose transitive in both). Only `authd` (sign side)
imports go-jose, to sign and marshal the JWK Set.

## Revocation = short-TTL-only (LOCKED)

**No revocation-list endpoint, no feed.** Verifiers stay fully offline and
never learn per-token revocation. Three cases cover everything:

- **Normal revoke** = wait for natural expiry (access tokens ~15 min; blast
  radius bounded by TTL).
- **Revoke a refresh token** (logout, "sign out everywhere") = delete its row
  in `refresh_tokens`. The access token it would have refreshed still works
  until its own `exp`; no new ones issue. Server-side state change at
  `authd`, not a verifier concern.
- **EMERGENCY revoke** (key compromise, "kill every token now") = **rotate
  the signing key** (§ JWK rotation). Every token signed by the retired `kid`
  fails verification once verifiers refresh the JWK Set (≤ JWKS cache TTL).

## Sessions — short access JWT + refresh token (LOCKED)

A login produces two tokens:

1. **Access JWT** — ES256, `typ: "user"`, ~15 min TTL, verified offline.
   Carried in `Authorization: Bearer` and (browser) `localStorage`.
2. **Refresh token** — opaque random 256-bit string (not a JWT), ~30 day TTL,
   **held and rotated at `authd`**, persisted as a SHA-256 hash in
   `refresh_tokens`. Delivered as an `HttpOnly` cookie. Lets the client get a
   fresh access JWT via `POST /v1/refresh` without re-running OAuth.

**Refresh-token rotation (one-time-use).** Every `/v1/refresh` consumes the
presented token and issues a new refresh token + new access JWT; the consumed
row is marked `rotated_to` → successor. Presenting an already-rotated token is
a reuse signal: `authd` invalidates the entire family (all rows in the
rotation chain) and returns 401. A missing/expired refresh token returns 401;
the client must re-login.

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

`auth/` slims to verify-only (`middleware.go` + new `jwks.go`/`mcp.go`). The
genuinely-new layer: ES256 + JWKS, the `/v1/*` API, service-token exchange,
refresh-token storage, `auth.db` schema.

**Current placement vs target.** `auth.Authorize` (`auth/authorize.go`)
is the row-based ACL gate ([`../4/9-acl-unified.md`](../4/9-acl-unified.md)).
arizuko's structural policy still lives in `auth/policy.go`
(`AuthorizeStructural`) — it moves to `grants/` once `gated` is removed
(gated still imports it). Tier-drop is staged: the data-plane decision
is scope- and folder-containment-based and consults no tier (the
uniform authorization lens, [`5-uniform-mcp-rest.md`](5-uniform-mcp-rest.md)
§ Authorization lens). Tier still gates management actions
(register/escalate/delegate/inject) inside `AuthorizeStructural`; that
tier check retires with the move-out, mapped to default scopes in one
place (`grants.DeriveRules`), not as decision branches.

## auth.db schema

`authd` owns `auth.db` — its own SQLite file + `migrations/` subdir
([`U-genericization.md`](U-genericization.md) DB-ownership rule), **not** in
gated's `messages.db`. Times are RFC3339 TEXT; all `*_hash` columns store hex
SHA-256, never the secret. Migrations run from `authd/migrations/*.sql` at
startup, same numbering as `store/migrations/`.

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
-- maps to at most one canonical user. (user_id, provider) is unique too —
-- at most one link per provider per user, so unlink-by-provider is
-- unambiguous (§ Account linking).
CREATE TABLE oauth_accounts (
  id            INTEGER PRIMARY KEY,
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL,           -- "google" | "github" | "discord" | "telegram"
  provider_sub  TEXT NOT NULL,           -- provider's stable user id
  email         TEXT,                    -- verified email at link time, if any
  email_verified INTEGER NOT NULL DEFAULT 0,
  linked_at     TEXT NOT NULL,
  UNIQUE(provider, provider_sub),
  UNIQUE(user_id, provider)
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
-- rows stay servable in the JWK Set through their overlap window
-- (retired_at + max access TTL), then the GC drops them. Revocation is
-- purely time-based — no permanent flag: an emergency revoke backdates
-- retired_at so the overlap has already elapsed, dropping the kid from
-- /v1/keys at once; the row then expires + is GC'd normally. private_key
-- encoding per § Private-key encryption-at-rest.
CREATE TABLE signing_keys (
  kid         TEXT PRIMARY KEY,          -- "<created-unix>-<8 hex rand>"
  alg         TEXT NOT NULL DEFAULT 'ES256',
  public_pem  TEXT NOT NULL,             -- PKIX public key PEM
  private_key TEXT NOT NULL,             -- "plain:<PKCS8 PEM>" or "gcm:v1:<b64 nonce||ct>" (§ Private-key encryption-at-rest)
  active      INTEGER NOT NULL DEFAULT 0, -- 1 = current signer; only one row =1
  created_at  TEXT NOT NULL,
  retired_at  TEXT                       -- set when rotated out; served until retired_at + overlap, then GC'd
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
`signing_keys` rows are swept by an hourly GC goroutine.

### Private-key encryption-at-rest

`signing_keys.private_key` is a tagged string (self-describing, no flag
column); reader dispatches on the `plain:` / `gcm:v1:` prefix:

- **`AUTHD_KEY_ENCRYPTION_KEY` unset** (single-host default):
  `"plain:" + <PKCS8 PEM>`. The DB file is the trust boundary.
- **set** (32-byte key, hex/base64): AES-256-GCM, `"gcm:v1:" + base64(
nonce(12) || ciphertext || tag(16) )` — standard `cipher.AEAD.Seal(nonce,
nonce, plaintext, nil)` with nonce prepended. Plaintext is the PKCS8 PEM;
  AAD is the `kid`. `v1` is the envelope version for forward-compat.

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

**Service-key seed mechanism.** `AUTHD_SERVICE_KEYS_FILE` points at a
compose-written JSON map `{ "<daemon>": { "secret_hash": "<hex>", "scope":
[...] } }`. On startup authd UPSERTs each entry into `service_keys` by
`daemon` (idempotent), so rotation = compose re-generates with new hashes and
authd re-seeds on restart. No admin API; the seed file is the single
provisioning path. Rows absent from the seed are left untouched (manual rows
survive).

## JWT claim set

Every minted token is an ES256 JWS, header
`{"alg":"ES256","typ":"JWT","kid":"<active kid>"}`. `kid` is **required**
(verifiers select the public key by it). The arizuko folder is a private
claim so `auth/` stays domain-agnostic.

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
- **`sub` prefix rule (pinned).** The `user:`/`service:` prefix appears
  **only** in the JWT `sub` claim. The **bare** canonical sub is what's
  stored everywhere else: all DB columns (`users.sub`,
  `refresh_tokens.user_sub`, `oauth_state.link_user_sub`), the grants
  lookup (`GET <GRANTS_URL>/v1/users/{sub}/scopes`), and the migration
  mapping. authd strips the prefix when calling grants and when ingesting
  `caller_sub` from routd/runed; it adds the prefix only when stamping the
  `sub` claim at mint time.

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

**DECISION (route naming).** Machine token + key endpoints live under
`/v1/*` (the typed API contract, `authd/api/v1/`); the human OAuth browser
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

### `DELETE /v1/users/me/accounts/{provider}` — unlink an OAuth account

Bearer = the user's own access JWT (the `sub` owns the `users` row). Removes
the matching `oauth_accounts` row. `{provider}` ∈
`{google,github,discord,telegram}`; `UNIQUE(user_id, provider)` means a user
holds at most one link per provider, so `(user_id, {provider})` resolves to
exactly one row and the path needs no `provider_sub`. Refused `409` when it
would leave the user with no `oauth_accounts` row and no local password
(`users.hash IS NULL`).

```jsonc
// DELETE /v1/users/me/accounts/google   Authorization: Bearer <user>
// 204  (no body)
// 404 {"error":"not_linked","message":"no google account on this user"}
// 409 {"error":"last_login_method","message":"cannot unlink the only login method"}
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

`authd` does not own grants — gated does
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
sweep) has no user in the loop but needs an identity to call gated/router: a
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

## Account linking + collision rules

One canonical `users` row may have multiple `oauth_accounts`. Rules (ported
from `oauth.go`/`collide.go`):

- **Uniqueness**: `(provider, provider_sub)` globally unique — one external
  identity → at most one canonical user. `(user_id, provider)` also unique —
  one link per provider per user, so unlink-by-`{provider}` is unambiguous.
- **First login** (no matching `oauth_accounts` row, no session): create a
  `users` row (`sub = "u_<rand>"`, `name` from provider), insert the
  `oauth_accounts` row, issue a session.
- **Returning login** (`(provider, provider_sub)` matches): resolve to its
  `user_id`, issue a session for that user's canonical `sub`.
- **Explicit link** (`intent=link`, carried via `oauth_state.link_user_sub`,
  set from the current session at `/auth/<provider>?intent=link`): new
  identity → insert row pointing at the current user. Identity **already
  linked to a different user** → **hard fail** with the collision screen; we
  never auto-merge two existing canonical users (the "Link" button is
  disabled). Merging populated accounts is an operator action, out of scope.
- **Implicit collision** (no `intent=link`, a session exists, the
  just-authenticated identity maps elsewhere or nowhere) → collision screen
  offering: link to the current account (only if unlinked), or log out and
  continue as the other.
- **Auto-link-by-verified-email: NO** — account-takeover vector if one
  provider's email verification is weaker. Linking is always explicit or
  first-login. `email`/`email_verified` are recorded only for audit + the
  email-allowlist gate (`GoogleAllowedEmails` / GitHub org), never for
  silent linking.
- **Collision-form key**: the `collide.go` form carries a short-lived signed
  `collideToken` (~10 min). Its HMAC key is `internal_keys` row
  `name='collide_hmac'` (§ schema), generated first-boot. Persisting it in
  `auth.db` (not memory) lets an in-flight form survive a restart and stay
  valid across DB-sharing authd instances. It's a CSRF token, not an identity
  token, so it stays symmetric and never leaves `authd`.
- **Unlink**: `DELETE /v1/users/me/accounts/{provider}` removes an
  `oauth_accounts` row; `409` if it's the user's last login method and they
  have no local password.

## Target shape — four library/daemon surfaces

A single Go module exposing four surfaces. `auth/` is verify-only;
`authd` is the daemon.

### 1. Verification primitives (library — every daemon)

```go
// Verify any incoming token against cached JWKs. Pure function once the
// JWKs cache is warm. ES256: picks the public key by `kid` from the JWS
// header (go-oidc RemoteKeySet). Pins iss=="authd", checks signature +
// exp/nbf. Does NOT enforce audience — these take no expected-aud arg; the
// token's `aud` lands in Identity.Audience (default "" = any) and the caller
// matches it with MatchesAudience when it cares.
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

`AuthHandlers` exposes only the four shared handlers; the per-provider
(`/auth/<provider>`, `/auth/<provider>/callback`), `/auth/telegram`, and
`/auth/collide` routes from the § OAuth routes table are **not** struct
fields. `Mount` registers them internally (one provider authorize+callback
pair per configured provider, plus telegram + collide), wiring each to the
shared `Callback`/`dispatchOAuth` path. Daemons that want the full login
surface call `Mount`; `Handlers` is for embedding the four core handlers
into a custom mux.

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

`mint_token` forwards to `authd /v1/tokens` (downscope mode); the host never
signs. This makes agent → sub-agent delegation safe without admin in the
loop: the agent's own token is the parent, `authd` signs a narrower token.
The MCP host (gated's ipc subsystem today; `mcpd` after the split) mounts
these alongside its tools.

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

Only `authd` holds the private key and mounts `auth.Mount` (OAuth + mint).
Every other daemon carries public JWKs and verifies offline.

## HMAC retirement plan

The authd cutover retires the two remaining shared symmetric secrets.
One-shot, no dual-secret period.

| Secret today                                                     | Used for                                                                   | Replaced by                                                                                                                                             |
| ---------------------------------------------------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `PROXYD_HMAC_SECRET` / `AUTH_SECRET` (`hmac.go` `VerifyUserSig`) | proxyd signing identity headers (`X-User-Sub`/`-Sig`) that backends verify | ES256 bearer token: proxyd verifies the authd-minted JWT and forwards `Authorization: Bearer`; backends verify the same JWT offline. `hmac.go` deletes. |
| `CHANNEL_SECRET` (`hmac.go` `VerifyChatSig`)                     | channel adapters signing chat-token headers (`X-Chat-Token`/`-Sig`)        | service token: each adapter exchanges `AUTHD_SERVICE_KEY` for a `service:<adapter>` JWT and presents it; gated verifies offline.                        |

After the cutover the only symmetric secrets left are the per-daemon
`AUTHD_SERVICE_KEY` bootstrap secrets and the `authd`-internal collision-CSRF
key. `auth/middleware.go` `RequireSigned`/`StripUnsigned` is re-pointed from
HMAC-header to JWKs-bearer verification in the same cutover.

## What this spec is not

- Not distributed minting (`authd` is the sole signer; backends verify).
- Not symmetric token crypto (ES256 from launch; daemons hold only public
  JWKs; bootstrap secrets are exchange credentials, not token signers).
- Not a public OAuth/OIDC authorization server — internal mint, no client
  registration / consent / introspection.
- Not a per-token revocation list or feed (§ Revocation).
- Not issuer-side OIDC conformance — we are an OIDC relying party; our issued
  tokens are plain ES256 JWTs.

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

After step 2 `authd` signs and JWKs are live; after step 4 every backend
verifies offline and HMAC is gone. The rest of the gated split follows later.

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
- `gated/ipc/...` (or `mcpd` after the split) — registers `auth.MCPTools`;
  `mint_token` forwards to `authd`.
