---
status: shipped
---

# auth: central authority daemon + offline-verify library

> **Shipped.** authd is the sole ES256 minter — holds the private key, serves
> JWKS at `/v1/keys`, runs the OAuth login flow, issues `service:<daemon>` +
> user tokens, rotates refresh tokens. Every other daemon offline-verifies
> against cached JWKS via the `auth/` library; none mint. HMAC identity is
> **fully retired** — `PROXYD_HMAC_SECRET`/`CHANNEL_SECRET` are gone, ES256
> service tokens carry every inter-daemon identity (see CHANGELOG). The only
> symmetric secret left is the OAuth CSRF-state HMAC (a CSRF token, not
> identity). Deferred, non-blocking: the `/v1/keys/rotate` endpoint + `authd
rotate-key` CLI ([`39-auth-api.md`](39-auth-api.md) § JWK rotation has the
> mechanism; short-TTL + redeploy rotates today).

**DECISION.** Token authority is centralized in a single `authd` daemon —
the **sole signer**. `authd` mints every token, holds the ES256 private key,
and publishes public JWKs at `/v1/keys`. Every other daemon
**offline-verifies** against cached JWKs via the `auth/` library; no daemon
mints its own. Distributed / self-minting is rejected. Verification is a pure
function over `(token, JWKs)` — no per-request hop, and `authd` being briefly
down doesn't stop verification of already-issued tokens. The single issuer is
the one place to record issuance and rotate the signing key (the
emergency-revoke lever, below).

Two artifacts:

- **`authd`** — the daemon. Owns the ES256 private key, `auth.db`, the OAuth
  login flow, token issuance, refresh-token rotation, JWKs publication. The
  one process that can sign.
- **`auth/`** — the library. Offline verification, scope-check, JWKs-cache
  refresh, mountable OAuth handlers, MCP tool handlers. Every daemon imports
  it; none sign. The library IS authd's published client contract — there is no
  separate `authd/api/v1` package.

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
  the signing key** ([`39-auth-api.md`](39-auth-api.md)). Every token signed by the retired `kid`
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

## auth.db schema

`authd` owns `auth.db` — its own SQLite file + `migrations/` subdir
(each daemon owns its own DB), separate from the message store. Times are
RFC3339 TEXT; all `*_hash` columns store hex
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

There is no `tier` field — scopes replace tier everywhere.

The `/v1/*` token/key endpoints, JWK rotation, TTL table, and service bootstrap
live in [`39-auth-api.md`](39-auth-api.md). The OAuth `/auth/*` flow + the
`auth/` library surfaces are below.

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
routd hosts the MCP socket in-process (`ServeTurnMCP`) and mounts
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

## What this spec is not

- Not distributed minting (`authd` is the sole signer; backends verify).
- Not symmetric token crypto (ES256 from launch; daemons hold only public
  JWKs; bootstrap secrets are exchange credentials, not token signers).
- Not a public OAuth/OIDC authorization server — internal mint, no client
  registration / consent / introspection.
- Not a per-token revocation list or feed (§ Revocation).
- Not issuer-side OIDC conformance — we are an OIDC relying party; our issued
  tokens are plain ES256 JWTs.

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
- `compose/compose.go` — seed `service_keys` + write per-daemon
  `AUTHD_SERVICE_KEY`; aggregate `service_scope` from
  `template/services/*.toml`.
- `proxyd/main.go` — delegates login to `authd`; verifies via
  `auth.FetchKeys`; no local mint; HMAC header path deleted.
- `routd/mcp.go` (in-process MCP host) — registers `auth.MCPTools`;
  `mint_token` forwards to `authd`.
