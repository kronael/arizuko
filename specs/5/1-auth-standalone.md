---
status: draft
---

> **2026-05-28.** Resolved design. `authd` daemon + `auth/` library is
> the chosen shape; the earlier "library-only, no daemon" framing is
> dropped — it can't host revocation. **Not yet implemented in code**:
> `auth/` is a library today (`middleware.go` `RequireSigned` /
> `StripUnsigned`, plus arizuko-domain handlers); there is no `authd`
> binary and no `auth.db`. This spec describes the decided target, not a
> proposal to debate. Build order in § One-shot migration plan.

# authd daemon + auth/ library

`authd` is **the place where identity is created, verified, and
retracted**: a daemon that owns `auth.db`, hosts the OAuth flow, signs
JWTs, holds the revocation list, owns account linking, and publishes
verification keys (JWKs) at `/v1/keys`. It is **paired with** the
`auth/` library that every other daemon imports for **offline** JWT
verification, scope checks, and mountable middleware.

The split is deliberate. **Daemon for authority, library for
verification.** Identity authority must live in one process —
otherwise revocation is impossible, the OAuth flow is duplicated, and
the JWT signing key is sprayed across daemons. Verification must live
in every process — a network hop per request is unacceptable, and
every backend depending on authd's uptime is a fault-domain explosion.

This is the **first instance of the published-contract pattern** from
[U-genericization.md](U-genericization.md) § _Per-service
`<daemon>/api/v1/`_: `authd/api/v1/` is the types-only sub-package that
external code (including `auth/`) imports; internal `authd/handler.go`,
`authd/db.go` stay off-limits to other daemons.

## Why daemon AND library

A pure library can't:

- **Revoke.** Revocation needs a central authority because verifiers
  are distributed. A library-only design relies on short TTLs —
  workable for ephemeral agents, fragile for long sessions and stolen
  tokens. This is the killer unlock: without authd, leaked JWTs are
  valid until TTL; with authd, ops retract any token immediately and
  every verifier picks it up on its next revocation-list refresh.
- **Centralize OAuth.** The dance with Google/GitHub/OIDC needs HTTP
  endpoints, pending state, and one callback URL registered with the
  provider. Per-daemon OAuth means N callback URLs, N copies of client
  secrets, N pending-state stores.
- **Own account linking.** "Same user, two providers → one account" is
  a database write. A library can read the link table via interface,
  but the writer must be one process.

A pure daemon can't:

- **Verify cheaply.** Every JWT verify becoming a round trip to authd
  is untenable for hot paths (every signed request, every MCP call).
  Distributed offline verification is the whole point of JWTs (~µs
  local vs ~ms network).
- **Mount on existing muxes.** Daemons need `RequireSigned`
  middleware, `whoami` / `mint_token` MCP tools, and scope predicates
  registered locally without a runtime dependency on authd.

Layered together: authd is the **authority** (central state, single
signer); `auth/` is the **verifier** (distributed, offline, fast). The
seam is **public-key distribution** — authd publishes its verification
key at `/v1/keys`; `auth/` fetches and caches it, then verifies
locally with zero network IO. The N-daemon system pays one network hop
per JWKs refresh per daemon (~1h), not one per request.

## Kerberos-inspired, not Kerberos

The pattern is borrowed from Kerberos: a central authority (KDC) mints
tickets that distributed services verify offline. We borrow the
**architecture**:

- Central authority for state operations (mint, revoke, OAuth).
- Offline-verifiable tickets (Kerberos signs with service-shared keys;
  we sign JWTs with authd's private key, verified against its
  published public key).
- Centrally-handled key rotation.

We don't borrow the **protocol**. KRB5 is the wrong shape for arizuko:
browsers don't speak it, SPNEGO/Negotiate is enterprise-desktop-coupled
and a config nightmare, and keytabs/realm-sync pay off only in AD
shops. **OAuth + JWT is what consumers already use** — Google, GitHub,
every OIDC provider speaks OAuth code-flow; JWTs are the standard
bearer shape. The protocol of the day, not the protocol of 1988.

## `authd` daemon

### Operations

| Operation                                   | Today                                        | After authd                                                           |
| ------------------------------------------- | -------------------------------------------- | --------------------------------------------------------------------- |
| OAuth dance with Google/GitHub              | proxyd hosts `/auth/login`, `/auth/callback` | authd hosts them; proxyd 302s to authd.                               |
| Mint JWT for authenticated user             | proxyd signs locally with `AUTH_SECRET`      | authd is sole signer; private key in `auth.db`.                       |
| Verify JWT on every web request             | each daemon HMAC-verifies via `AUTH_SECRET`  | each daemon offline-verifies via authd's public key (cached locally). |
| Mint service-to-service token               | shared `CHANNEL_SECRET` bearer               | authd issues per-service JWTs at adapter boot.                        |
| Revoke a token before TTL                   | impossible (bearer-valid until TTL)          | authd revocation list; verifiers check it on every verify (cached).   |
| Link OAuth providers to one account         | gated's `account_links` table                | authd owns `account_links`; gated reads via `authd/api/v1/accounts`.  |
| Sign service identity for cross-daemon HTTP | `PROXYD_HMAC_SECRET` over `X-User-*` headers | authd-minted JWT in `Authorization: Bearer`.                          |

### `auth.db` schema

authd owns its own SQLite file. Migrations live in `authd/migrations/`.
Per [U-genericization.md](U-genericization.md) § _Database ownership
rule_, no other daemon touches this file; cross-daemon reads go through
`authd/api/v1/`.

```sql
-- Identity records.
CREATE TABLE users (
    sub        TEXT PRIMARY KEY,    -- arizuko-side stable sub
    created_at INTEGER NOT NULL,
    name       TEXT,
    email      TEXT
);

-- Active sessions (web cookies, etc). Optional persistence.
CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    sub        TEXT NOT NULL,
    issued_at  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    user_agent TEXT
);

-- Issued tokens (for revocation lookup; not the JWT itself).
CREATE TABLE tokens (
    jti        TEXT PRIMARY KEY,    -- JWT id claim
    sub        TEXT NOT NULL,
    scope      TEXT NOT NULL,
    audience   TEXT,
    issued_at  INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

-- Revocation list. Verifiers cache + check.
CREATE TABLE revocations (
    jti        TEXT PRIMARY KEY,
    revoked_at INTEGER NOT NULL,
    reason     TEXT
);

-- OAuth pending state (login → callback handoff).
CREATE TABLE oauth_states (
    state      TEXT PRIMARY KEY,
    provider   TEXT NOT NULL,
    return_to  TEXT,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL    -- short TTL (~10min)
);

-- Provider linking: one arizuko sub, multiple provider identities.
CREATE TABLE account_links (
    provider     TEXT NOT NULL,
    provider_sub TEXT NOT NULL,
    sub          TEXT NOT NULL,    -- arizuko sub
    linked_at    INTEGER NOT NULL,
    PRIMARY KEY (provider, provider_sub)
);

-- Signing keys (rotation support; kid in JWT header selects key).
CREATE TABLE keys (
    kid          TEXT PRIMARY KEY,
    alg          TEXT NOT NULL,    -- "ES256" / "RS256" / "HS256"
    public_pem   TEXT,             -- null for HMAC
    private_pem  TEXT NOT NULL,    -- secret (HMAC) or private key (ECDSA/RSA)
    created_at   INTEGER NOT NULL,
    activated_at INTEGER,
    retired_at   INTEGER
);
```

`keys` supports rotation: a new key lands, `/v1/keys` exposes both, the
old key retires once every token signed by it has expired.

### `authd/api/v1/` published contract

Per the per-service convention in
[U-genericization.md](U-genericization.md), authd publishes its wire
shapes in `authd/api/v1/types.go` + `authd/api/v1/client.go`. The
package has zero behavior, zero arizuko-internal imports beyond
`types/`, and is imported freely by other daemons and the `auth/`
library.

```go
package v1

import (
    "time"

    "github.com/kronael/arizuko/types"
)

// Claims is the full claim set on every authd-issued JWT.
type Claims struct {
    Sub      types.UserSub  `json:"sub"`
    Scope    []types.Scope  `json:"scope"`
    Audience string         `json:"aud,omitempty"`
    Issuer   string         `json:"iss"`               // "authd"
    JTI      string         `json:"jti"`               // for revocation
    IssuedAt int64          `json:"iat"`
    Expires  int64          `json:"exp"`
    Extra    map[string]any `json:"x,omitempty"`       // app-specific opaque
}

// MintRequest issues a new token.
type MintRequest struct {
    Sub      types.UserSub  `json:"sub"`
    Scope    []types.Scope  `json:"scope"`
    Audience string         `json:"aud,omitempty"`
    TTL      time.Duration  `json:"ttl"`
    Extra    map[string]any `json:"x,omitempty"`
}

type MintResponse struct {
    Token   string `json:"token"`     // signed JWT
    JTI     string `json:"jti"`
    Expires int64  `json:"exp"`
}

// MintNarrowerRequest downscopes an existing token. Server validates
// the requested scope is a strict subset of the parent's scope.
type MintNarrowerRequest struct {
    ParentToken string         `json:"parent"`
    Scope       []types.Scope  `json:"scope"`
    TTL         time.Duration  `json:"ttl"`
    Extra       map[string]any `json:"x,omitempty"`
}

// RevokeRequest retracts a token by JTI before its TTL.
type RevokeRequest struct {
    JTI    string `json:"jti"`
    Reason string `json:"reason,omitempty"`
}

// ListRequest enumerates active tokens for an operator.
type ListRequest struct {
    Sub      types.UserSub `json:"sub,omitempty"`
    Audience string        `json:"aud,omitempty"`
}

// JWKsResponse publishes verification keys. Daemons cache; rotate via
// the kid header in the JWT.
type JWKsResponse struct {
    Keys []JWK `json:"keys"`
}

type JWK struct {
    Kid string `json:"kid"`
    Alg string `json:"alg"`
    Kty string `json:"kty"`   // "EC" / "RSA" / "oct"
    Use string `json:"use"`   // "sig"
    Crv string `json:"crv,omitempty"`   // ECDSA
    X   string `json:"x,omitempty"`
    Y   string `json:"y,omitempty"`
    N   string `json:"n,omitempty"`     // RSA
    E   string `json:"e,omitempty"`
}

// RevocationListResponse is the snapshot daemons fetch on cache
// refresh. Pagination + delta endpoints exist for large lists.
type RevocationListResponse struct {
    Revoked    []string `json:"revoked"`    // JTIs
    AsOf       int64    `json:"as_of"`
    NextCursor string   `json:"next,omitempty"`
}
```

```go
// authd/api/v1/client.go — thin HTTP wrapper, no state beyond baseURL.
type Client struct{ baseURL string; hc *http.Client }

func NewClient(baseURL string) *Client

func (c *Client) Mint(ctx context.Context, req MintRequest) (MintResponse, error)
func (c *Client) MintNarrower(ctx context.Context, req MintNarrowerRequest) (MintResponse, error)
func (c *Client) Revoke(ctx context.Context, req RevokeRequest) error
func (c *Client) List(ctx context.Context, req ListRequest) (RevocationListResponse, error)
func (c *Client) JWKs(ctx context.Context) (JWKsResponse, error)
```

The contract is frozen at `v1`; `v2/` lives next to it when the shape
breaks.

HTTP surface, all under `/v1/` (auth ops) and `/auth/` (browser flow):

| Endpoint              | Method | Scope required          | Body                  |
| --------------------- | ------ | ----------------------- | --------------------- |
| `/v1/tokens`          | POST   | `authd:mint`            | `MintRequest`         |
| `/v1/tokens/narrower` | POST   | (parent token)          | `MintNarrowerRequest` |
| `/v1/tokens/{jti}`    | DELETE | `authd:revoke`          | `RevokeRequest`       |
| `/v1/tokens`          | GET    | `authd:admin`           | `ListRequest` (query) |
| `/v1/keys`            | GET    | none (public)           | —                     |
| `/v1/revocations`     | GET    | none (cached by verifs) | —                     |
| `/v1/accounts/{sub}`  | GET    | `authd:read`            | —                     |
| `/v1/links`           | POST   | (session)               | link request          |
| `/auth/login`         | GET    | none                    | provider redirect     |
| `/auth/callback`      | GET    | none                    | OAuth code exchange   |
| `/auth/logout`        | POST   | (session)               | clears cookie         |
| `/auth/me`            | GET    | (session)               | identity              |

Mutating `/v1/*` endpoints accept `X-Idempotency-Key`; every endpoint
propagates `X-Turn-Id` if set (both per
[U-genericization.md](U-genericization.md) § _Open detail-level
items_).

OAuth handlers move from today's `auth/oauth.go` to `authd/oauth.go`
verbatim — same provider list, same redirect-URI shape. The change is
only ownership: proxyd no longer mounts them; authd does.

## `auth/` library

The library every daemon imports. Per
[U-genericization.md](U-genericization.md)'s DAG it sits at Layer 1
(depends on Layer 0 — `types/` for IDs and `authd/api/v1/` for shared
shapes).

### Surface

```go
package auth

import (
    "context"
    "net/http"

    "github.com/kronael/arizuko/authd/api/v1"
    "github.com/kronael/arizuko/types"
)

// Verifier verifies tokens offline using cached JWKs + revocation
// list. One per daemon; goroutine-safe.
type Verifier interface {
    // Verify parses, signature-checks, expiry-checks, and revocation-
    // checks a token. Pure-offline once warm; returns Claims on success.
    Verify(ctx context.Context, token string) (*v1.Claims, error)

    // RefreshJWKs / RefreshRevocations force a reload. Called
    // automatically on the refresh interval; exposed for tests + ops.
    RefreshJWKs(ctx context.Context) error
    RefreshRevocations(ctx context.Context) error
}

// NewVerifier constructs a Verifier pointed at an authd instance.
func NewVerifier(authdURL string, opts ...Option) Verifier

// Scope-check primitives. Pure functions.
func HasScope(c *v1.Claims, scope types.Scope) bool
func MatchesAudience(c *v1.Claims, aud string) bool

// Mountable middleware. RequireSigned rejects requests without a valid
// bearer (401), attaching *v1.Claims to the request context.
// StripUnsigned attaches Claims if present + valid, otherwise strips
// the Authorization header (no rejection) — for mixed public/private
// routes.
func RequireSigned(v Verifier) func(http.Handler) http.Handler
func StripUnsigned(v Verifier) func(http.Handler) http.Handler

// ClaimsFromContext retrieves the Claims attached by the middleware.
func ClaimsFromContext(ctx context.Context) (*v1.Claims, bool)
```

### Offline JWT verify

The library caches authd's JWKs at startup and refreshes every ~1h
(configurable). On every `Verify`:

1. Parse JWT header → extract `kid`.
2. Look up `kid` in cached JWKs. If missing, force-refresh once; if
   still missing, reject.
3. Signature-check against the public key.
4. Check `exp` + `nbf`.
5. Check `jti` against the cached revocation list. If listed, reject.
6. Return Claims.

Cache refresh:

- JWKs: pull every `JWKS_REFRESH_INTERVAL` (default 1h).
- Revocations: pull every `REVOCATION_REFRESH_INTERVAL` (default 30s) —
  the trade-off between staleness and load. A revoked token verifies
  for at most one poll interval; operators run the poll faster if
  subsecond revocation matters.

Both refresh in the background; verify never blocks on a network call.
If a refresh fails, the verifier serves stale data and surfaces the
staleness in `obs/` metrics — existing tokens stay valid even if authd
is down; only new mints fail.

### Mountable middleware

Every daemon that gates requests imports `auth` and wraps its mux:

```go
v := auth.NewVerifier(os.Getenv("AUTHD_URL"))
mux := http.NewServeMux()
mux.Handle("/v1/", auth.RequireSigned(v)(myHandler))
mux.Handle("/public/", auth.StripUnsigned(v)(myHandler))
```

Three lines per daemon for full authz: no HMAC env var, no
shared-secret coordination — only `AUTHD_URL` plus the JWKs trust
chain.

### MCP tool handlers

`auth.MCPTools(v Verifier, c *v1.Client) []MCPTool` returns the
agent-side tool definitions. The hosting daemon (today gated's ipc
subsystem; tomorrow `mcp-hostd`) iterates and registers each with its
MCP server.

| Tool             | Purpose                                                     | Scope required            |
| ---------------- | ----------------------------------------------------------- | ------------------------- |
| `whoami`         | Return the caller's own Claims                              | none                      |
| `mint_token`     | Mint a token narrower than caller's own scope (calls authd) | (downscope-only enforced) |
| `verify_token`   | Introspect a token (parses? what scope? revoked?)           | any valid token           |
| `revoke_token`   | Revoke a token by JTI (calls authd)                         | `authd:revoke`            |
| `list_providers` | List configured OAuth providers                             | none                      |

`whoami` and `verify_token` are local (offline). `mint_token` and
`revoke_token` delegate to authd. `mint_token` enforces downscope-only
server-side via `/v1/tokens/narrower`: authd validates the parent
token's scope and rejects any minted scope broader than the parent's.
The client-side tool is a thin wrapper, never the enforcement point.

## proxyd's evolved role

After authd lands, proxyd **shrinks** to HTTP proxy + vhost routing +
rate-limit + auth middleware:

| Today                                            | After authd                                                |
| ------------------------------------------------ | ---------------------------------------------------------- |
| Hosts `/auth/login` + `/auth/callback`           | 302s to `authd/auth/login` with return-to; no OAuth logic. |
| Signs JWTs locally with `AUTH_SECRET`            | Holds no signing key; authd is the sole signer.            |
| Verifies JWTs via shared `AUTH_SECRET`           | Uses `auth/`; offline-verifies via authd's JWKs.           |
| Signs identity headers with `PROXYD_HMAC_SECRET` | **Deleted**. Backends verify the JWT directly via `auth/`. |

[35-proxyd-standalone.md](35-proxyd-standalone.md) locks `[auth].mode =
"library"` for its v1 with `"remote"` (call authd) as the future hook.
Post-authd, `"remote"` becomes the default and `"library"` retires —
see that spec § _Decisions_. The four `folder` references in
`proxyd/main.go` retire in the same release per
[U-genericization.md](U-genericization.md)'s proxyd row.

## HMAC retirement plan

Two HMAC mechanisms retire in the authd release, both deleted in the
same commit that brings authd up (per § One-shot migration plan).

**`PROXYD_HMAC_SECRET`** (proxyd → backend identity headers). Today
proxyd signs `X-User-Sub` / `X-User-Name` / `X-User-Groups` with
`PROXYD_HMAC_SECRET` and attaches `X-User-Sig`; backends verify via
`auth/middleware.go`. After authd, backends receive the original JWT in
`Authorization: Bearer` (proxyd passes it through) and offline-verify
via `auth/` against authd's JWKs. The `X-User-*` headers retire; daemons
read from `auth.ClaimsFromContext(ctx)`.

**`CHANNEL_SECRET`** (adapter → gateway bearer). Today every channel
adapter sends a shared `CHANNEL_SECRET` bearer; gated checks string
equality. After authd, each adapter is provisioned at boot (via
compose-generation) with an authd-minted **service JWT** carrying
scopes like `gateway:write`, `audience: gateway`. Gated verifies via
`auth/`. Distinct adapters carry distinct subs; gated can rate-limit or
revoke per-adapter; rotating one adapter's credentials doesn't affect
others.

## One-shot migration plan (NO BACKWARD COMPATIBILITY)

Per CLAUDE.md § _Minimality and orthogonality_ and the directive in
[U-genericization.md](U-genericization.md) § _NO BACKWARD
COMPATIBILITY_, the cutover is **one-shot**: no dual-API period, no
parallel codepaths, no compatibility layer. Recovery is `git revert`,
not a fallback path.

Build order — each step lands as its own commit; the cutover
(steps 4–6) ships as one release:

1. **Build `authd/api/v1/` + `auth.db` schema.** Types, client stub,
   migrations. Compiles; no behavior yet.
2. **Implement `authd`.** Handlers, OAuth flow (moved from
   `auth/oauth.go`), JWT signing (HMAC initially; ECDSA via `kid`
   rotation post-launch), revocation list, `/v1/keys`, account-links.
   Run alongside proxyd in a test deployment; not yet wired in prod.
3. **Implement `auth/` verify.** Offline `Verify` with JWKs + revocation
   caches, `authd/api/v1/` client, `RequireSigned` / `StripUnsigned`
   rewired against the `Verifier` (drops the HMAC path), context helpers.
4. **Cut every daemon over to `auth/` for verify.** Same commit deletes
   each daemon's per-daemon JWT-verification helper; every daemon points
   at `AUTHD_URL`.
5. **Delete proxyd's OAuth handlers + HMAC signing.** Same commit:
   `/auth/login` becomes a 302 to authd; proxyd verifies via `auth/`;
   `PROXYD_HMAC_SECRET` + the `X-User-Sig` computation delete.
6. **Delete `CHANNEL_SECRET`.** Same commit: adapters receive
   authd-minted service JWTs at boot; gated verifies via `auth/`.
7. **Ship as one release.** Migration broadcast via the migrate skill
   (CLAUDE.md § _Shipping changes_).

Test gate before tagging: `make test-e2e` green (every daemon's
protected endpoints work with authd-minted tokens) and `make smoke
SMOKE_INSTANCE=krons` passes post-deploy.

## What this spec is not

- **Not "auth as a library only"** — that prior framing can't host
  revocation. The library still exists; the daemon now exists too.
- **Not centralized verification.** Daemons verify offline against
  cached JWKs. authd is the authority, not the synchronous verifier.
- **Not full OIDC conformance.** The login flow is OAuth
  code-exchange → JWT issuance — enough for arizuko + most deployments.
- **Not multi-key per audience.** Audience is a routing field, not a
  key separator; one signing key (with `kid` rotation) covers all
  audiences.
- **Not a service mesh.** JWTs travel in `Authorization: Bearer`; no
  mTLS, no service discovery.
- **Not a staged migration.** The cutover is one release.

## Open

- **JWKs emergency rotation.** Daemons cache JWKs ~1h. A compromised
  key needs a faster pull or a push. Lean: a short-TTL `keys-changed`
  flag in the revocation-list response; daemons pull JWKs eagerly when
  set.
- **Revocation list GC.** Cron in authd purges revocations older than
  `max_token_ttl`; rows past TTL fail expiry-check anyway. (Same item
  noted in [U-genericization.md](U-genericization.md).)
- **Service-account provisioning UX.** Adapters get service JWTs at
  boot via compose-generation: an env var carries a one-shot bootstrap
  token the adapter trades for a long-lived service JWT on first start.
  Detail per-adapter.
- **`mint_token` over MCP — agent as bearer.** The agent holds its own
  token. On `mint_token`, authd uses that token as the parent for
  downscope; the minted token returns to the agent, which decides who
  to hand it to (typically a sub-agent it spawns).
- **Wildcard scopes.** `*:*` allows everything, `tasks:*` any task
  verb. Match logic lives in `HasScope`; namespace-wildcards only for
  now, richer matching later.
- **Multi-instance authd.** v1 is single-instance. Horizontal scale is
  post-v1 (shared `auth.db` or active-passive failover).

## Implementation pointers

- `authd/` (new) — daemon binary: `main.go`, `handler.go`,
  `oauth.go` (moved from `auth/oauth.go`), `link.go`, `db.go`,
  `migrations/`.
- `authd/api/v1/` (new) — published contract: `types.go` + `client.go`.
- `auth/verify.go` (new) — `Verifier`, JWKs cache, revocation cache,
  offline `Verify`.
- `auth/middleware.go` — existing `RequireSigned` / `StripUnsigned`
  rewired against `Verifier` (drops the HMAC path).
- `auth/mcp.go` (new) — `MCPTools` returning the agent-side tools.
- `auth/acl.go`, `auth/policy.go`, `auth/identity.go` —
  arizuko-domain ACL evaluation; not part of authd. Stay in `auth/`
  (or move to `grants/`) but consume `types.Scope`, not
  `core.Folder` / `core.Tier`.
- `proxyd/main.go` — OAuth handlers + HMAC delete; uses
  `auth.NewVerifier` like every other daemon.
- `gated/middleware.go` — `CHANNEL_SECRET` string-equality check
  deletes; uses `auth.RequireSigned`.
- `compose/compose.go` — service-JWT provisioning for adapters;
  `AUTHD_URL` injection.

## Cross-references

- [U-genericization.md](U-genericization.md) — naming, DAG layering,
  `types/`, `<daemon>/api/v1/`, DB-ownership, NO BACKWARD COMPATIBILITY.
  authd is the first instance of the published-contract pattern.
- [35-proxyd-standalone.md](35-proxyd-standalone.md) — the consumer
  that switches from local mint to authd-client mint; `[auth].mode`.
- [5-uniform-mcp-rest.md](5-uniform-mcp-rest.md) — the federated
  control API that consumes authd-minted tokens + scope vocabulary.
- [N-oauth-services.md](N-oauth-services.md),
  [`11/14-surrogate-oauth.md`](../11/14-surrogate-oauth.md) —
  third-party OAuth (Gmail / GitHub / …) flows through this same
  pattern, with authd as the host.
- [A-orthogonal-components.md](A-orthogonal-components.md) — the
  import-graph discipline `authd/api/v1/` enforces.

## Blueprint takeaway

The pattern this spec lands on, reusable for future extractions:

1. Decide whether the component needs an authority (centralized state,
   signing, OAuth host). If yes — make it a daemon.
2. Decide whether every consumer needs offline access (no network hop
   per request). If yes — pair the daemon with a library that operates
   against cached state.
3. Publish the daemon's wire contract under `<daemon>/api/v1/` so
   others call it without reaching into internals.
4. The daemon is the only writer to its own state; every reader
   (including the library) consumes via `api/v1/` or cached public-key
   material.

Auth fits this — authority for identity, offline verify everywhere.
`timed`, `routerd`, `agent-runnerd` fit the same shape when extracted.
