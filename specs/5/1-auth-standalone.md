---
status: partial
---

# auth: central authority daemon + offline-verify library

**Decided.** Token authority is centralized in a single `authd`
daemon — the **sole signer**. `authd` mints and revokes every token,
holds the signing key, and publishes public JWKs at `/v1/keys`. Every
other daemon **offline-verifies** tokens against cached JWKs using the
`auth/` library; no daemon mints its own tokens. Distributed /
self-minting is rejected.

The split is two artifacts:

- **`authd`** — the daemon. Owns the private signing key, the OAuth
  login flow, token issuance, revocation, and JWKs publication. The
  one process that can sign.
- **`auth/`** — the library. Offline verification, scope-check,
  JWKs-cache refresh, mountable middleware, and MCP tool handlers.
  Every daemon imports it; none of them sign.

This is **extracted standalone first** — `authd` is the first piece of
the gated split, shipped on its own, proving the `<daemon>/api/v1/` +
`types/` pattern before `routerd`/`agent-runnerd`/`mcp-hostd` follow in
a later release (sequencing: [`U-genericization.md`](U-genericization.md)
"gated split").

## Why a central signer

A single signer is the load-bearing decision; everything else follows:

- **One key, one issuer.** Only `authd` holds the ES256 private key.
  Compromise surface and rotation are confined to one process; no
  daemon can forge a token because none can sign.
- **Offline verification, no hot-path hop.** Verification is a pure
  function over `(token, JWKs)`. Daemons cache `authd`'s public JWKs
  and verify in-process — no network call per request. `authd` being
  briefly down does not stop verification of already-issued tokens.
- **Centralized revocation + audit.** The single issuer is the single
  place to revoke and to record issuance — impossible to colocate
  cleanly when every daemon mints its own.

Verification stays a library function so it has zero network cost;
_signing_ is the daemon's exclusive job.

## Today's `auth/`

```
auth/
  hmac.go             generic — HMAC sign/verify of identity headers
  jwt.go              generic — JWT structure (planned mint API)
  oauth.go            generic — OAuth provider abstraction
  middleware.go       generic — RequireSigned / StripUnsigned guards
  web.go              generic — login/callback/logout HTTP handlers
  routes.go           generic — provider route registration
  link.go             generic — multi-provider account linking
  collide.go          generic — handles two providers landing on same user
  ───
  acl.go              arizuko — folder/scope ACL evaluator
  policy.go           arizuko — policy composition for arizuko grants
  identity.go         arizuko — identity model with folder field
```

`acl.go`, `policy.go`, `identity.go` move out (see Target shape).
The rest stays.

## Target shape

A single Go module exposing four surfaces:

### 1. Verification primitives (library — every daemon)

```go
// Verify any incoming token against cached JWKs. Pure function, no IO
// once the JWKs cache is warm. ES256: the verifier picks the public
// key by `kid` from the token header.
func VerifyHTTP(r *http.Request, jwks *KeySet) (Identity, error)
func VerifyToken(token string, jwks *KeySet) (Identity, error)

// Scope check primitives. Authorization is scope-match; there is no
// tier.
func HasScope(ident Identity, resource, verb string) bool
func MatchesAudience(ident Identity, aud string) bool

// JWKs cache: fetches authd's public keys from /v1/keys and refreshes
// on `kid` miss or TTL. Verification never needs the private key.
type KeySet struct{ /* kid → ed/ecdsa public key */ }
func FetchKeys(authdURL string) (*KeySet, error)
```

### 2. Mint primitives (authd only — the sole signer)

Minting lives **only** in `authd`, which holds the ES256 private key.
No other daemon links a signing path; the library exposes mint as the
internal building block `authd` uses, never a key-taking function other
daemons can call:

```go
// Inside authd. Signs with the ES256 private key authd holds.
func (a *Authd) Mint(claims Claims, ttl time.Duration) (string, error)

// Downscope: mint a narrower token from an existing one. Backs the
// `mint_token` MCP tool, which authd serves (or which forwards to
// authd). Errors if requested scope is broader than the parent's.
func (a *Authd) MintNarrower(parent Identity, claims Claims, ttl time.Duration) (string, error)
```

`Claims` and `Identity` carry only generic fields:

```go
type Claims struct {
    Sub      string                 // subject (user, agent, key)
    Scope    []string               // capability list ("tasks:write", ...)
    Audience string                 // optional, multi-app deployments
    Extra    map[string]string      // app-specific opaque fields
    TTL      time.Duration
    Issuer   string                 // always "authd" — the sole signer
}

type Identity struct {
    Sub      string
    Scope    []string
    Audience string
    Extra    map[string]string      // includes "folder" for arizuko
    Issuer   string
    Expires  time.Time
}
```

Authorization is scope-based: `Identity.Scope` is the capability list,
checked with `HasScope`. There is no `tier` field (decision: scopes
replace tier everywhere — [`U-genericization.md`](U-genericization.md)
"Capability-vs-tier"). Folder and other arizuko concepts move into
`Extra`; the library doesn't know what those keys mean. Arizuko-specific
helpers live in `arizuko/identity.go` outside `auth/`.

### 3. OAuth flow handlers (mountable)

OAuth needs HTTP endpoints (`login`, `callback`, `logout`, `me`).
Those are exported as handlers any daemon can mount on its own mux:

```go
// Provider configuration.
type Provider struct {
    ID, Type, ClientID, ClientSecret, DiscoveryURL string
    // ...
}

// Returns a set of HTTP handlers ready to mount.
func Handlers(providers []Provider, key []byte, opts ...Option) AuthHandlers

type AuthHandlers struct {
    Login    http.HandlerFunc   // GET /auth/login
    Callback http.HandlerFunc   // GET /auth/callback
    Logout   http.HandlerFunc   // POST /auth/logout
    Me       http.HandlerFunc   // GET /auth/me
}

// Convenience: mount all four on a mux at the standard paths.
func Mount(mux *http.ServeMux, providers []Provider, key []byte, opts ...Option)
```

`authd` mounts these — it owns the OAuth login flow and is where the
session JWT is minted (only the signer can issue one). proxyd delegates
login to `authd` ([`35-proxyd-standalone.md`](35-proxyd-standalone.md)
"Login flow"); it enforces, it does not sign. The handlers stay in the
library so `authd` (and standalone non-arizuko deployments of it) mount
the same code.

Pending-state for OAuth (the short-lived `state` cookie/token between
`/auth/login` and `/auth/callback`) lives in-process with whichever
daemon mounted the handlers. Defaults to in-memory; pluggable
`StateStore` interface for ops needing redis or shared state.

Account-linking records (one user, multiple providers) are stored
via a pluggable `LinkStore` interface. Default: SQLite at a
configured path. Daemons that don't care about linking pass nil.

### 4. MCP tool handlers (mountable)

MCP tools for agents that need to introspect or mint sub-tokens:

```go
// Returns MCP tool definitions ready to register with any MCP server.
// Read-only tools (whoami, verify_token, list_providers) run in-process
// against cached JWKs. mint_token forwards to authd over HTTP — the
// host never signs.
func MCPTools(authdURL string, jwks *KeySet) []MCPTool

// MCPTool is a small struct: name, description, schema, handler.
// Hosting daemon iterates and registers each with its server.
```

| Tool             | Purpose                                        | Scope required            |
| ---------------- | ---------------------------------------------- | ------------------------- |
| `whoami`         | Return the caller's own Identity               | none                      |
| `mint_token`     | Mint a token narrower than caller's own scope  | (downscope-only enforced) |
| `verify_token`   | Introspect a token (does it parse, what scope) | any valid token           |
| `list_providers` | List configured OAuth providers                | none                      |

`mint_token` enforces downscope-only via `authd.MintNarrower`: the
agent can only issue tokens with a strict subset of its own scope, and
the actual signing happens in `authd`. This is the primitive that makes
agent → sub-agent delegation safe without admin in the loop.

The MCP host (gated's ipc subsystem today; `mcp-hostd` after the split)
mounts these alongside its existing tools. Read-only auth tools resolve
in-process against cached JWKs, no hop; `mint_token` forwards to `authd`
because only the signer can issue.

## Mounting pattern

A backend daemon verifies tokens — it does not sign. It caches `authd`'s
public JWKs and uses the library's middleware + MCP tools:

```go
import "github.com/kronael/arizuko/auth"

func main() {
    jwks, _ := auth.FetchKeys(os.Getenv("AUTHD_URL")) // public keys only

    // Verify-on-request middleware on the HTTP mux.
    mux := http.NewServeMux()
    mux.Handle("/v1/", auth.RequireSigned(jwks, handler))

    // MCP tools on the MCP server (if this daemon hosts one). mint_token
    // forwards to authd; read-only tools verify in-process.
    for _, tool := range auth.MCPTools(os.Getenv("AUTHD_URL"), jwks) {
        mcpServer.RegisterTool(tool)
    }

    // ... daemon's own routes and tools ...

    http.ListenAndServe(":8080", mux)
}
```

Backends carry the public JWKs and verify offline; the only daemon that
holds the private key and mounts `auth.Mount` (the OAuth login + mint
surface) is `authd`.

## Where each role lives, after this lands

| Role                                  | Where                                                        |
| ------------------------------------- | ------------------------------------------------------------ |
| Verify a token                        | Any daemon — `auth.VerifyHTTP` against cached JWKs, no hop   |
| Mint a token from claims              | `authd` only — the sole signer                               |
| Downscope an existing token           | `authd.MintNarrower` (MCP `mint_token` forwards to it)       |
| Publish public JWKs                   | `authd` at `/v1/keys`                                        |
| OAuth login + callback                | `authd` (mounts `auth.Handlers`); proxyd delegates           |
| MCP tools (`whoami`, `mint_token`, …) | gated's ipc subsystem / `mcp-hostd` (mounts `auth.MCPTools`) |
| Account-linking storage               | Pluggable; default SQLite via `LinkStore` (in `authd`)       |
| Pending OAuth state                   | Pluggable; default in-memory via `StateStore` (in `authd`)   |

## What this spec is not

- Not distributed minting. `authd` is the sole signer; no daemon
  self-mints. Backends only verify.
- Not symmetric crypto. ES256 (asymmetric) from launch — `authd`
  holds the private key, daemons hold only public JWKs. No HMAC
  shared-secret token path.
- Not full OIDC conformance. Login flow is JWT-issuing OAuth code
  exchange — enough for arizuko + most deployments.
- Not multi-key per audience for _verification routing_. `kid`
  selects the signing key for rotation; audience is for routing, not
  key separation.

## Implementation phases

`authd` is extracted **standalone first**, before the rest of the gated
split. It proves the `<daemon>/api/v1/` + `types/` pattern that
`routerd`/`agent-runnerd`/`mcp-hostd` adopt in a **later release**
(sequencing: [`U-genericization.md`](U-genericization.md) "gated split").

1. **Extract arizuko-specific code.** Move `acl.go`, `policy.go`,
   `identity.go` (folder fields; tier dropped) out of `auth/` into a new
   `arizuko/identity.go`. `auth/` left with only generic primitives.
   Pure refactor.

2. **Stand up `authd` with ES256.** Generate an ES256 keypair; `authd`
   holds the private key and serves `/v1/keys` (public JWKs, keyed by
   `kid`). Implement `Mint` + `MintNarrower` inside `authd`. Migrate
   proxyd's local JWT-mint to delegate to `authd /v1/tokens`.

3. **Refactor OAuth handlers as mountable; mount in `authd`.**
   `auth.Handlers` / `auth.Mount` registers the four routes; `authd`
   mounts them. Pluggable `StateStore` and `LinkStore` interfaces with
   default implementations.

4. **Offline verification everywhere.** Backends call `auth.FetchKeys`
   and verify against cached JWKs (no signing key on backends). MCP
   `mint_token` forwards to `authd`; gated's ipc subsystem
   (`mcp-hostd` after the split) mounts the read-only tools locally.

5. **Document for non-arizuko deployment.** `authd/README.md` +
   `auth/README.md` cover standalone `authd` + "verify with the library"
   usage. Examples for OAuth providers (Google/GitHub/OIDC), account
   linking, MCP integration.

After step 2 `authd` is the sole signer and JWKs are live. After step 4
every backend verifies offline. The rest of the gated split follows in a
later release.

## Code pointers

- `auth/hmac.go`, `auth/jwt.go`, `auth/oauth.go`, `auth/middleware.go`,
  `auth/web.go`, `auth/routes.go`, `auth/link.go`, `auth/collide.go` —
  keep, generic.
- `auth/acl.go`, `auth/policy.go`, `auth/identity.go` — move out
  to `arizuko/identity.go` (or similar).
- `auth/mcp.go` (new) — MCP tool definitions returned by `MCPTools`.
- `auth/store.go` (new) — `StateStore` and `LinkStore` interfaces +
  default implementations.
- `auth/jwks.go` (new) — `KeySet`, `FetchKeys`, ES256 verify-by-`kid`.
- `arizuko/identity.go` (new) — folder helpers reading from
  `Identity.Extra` (no tier).
- `authd/` (new daemon) — holds the ES256 private key, mounts
  `auth.Mount` for OAuth, serves `/v1/keys` (JWKs) and `/v1/tokens`
  (mint). The sole signer.
- `proxyd/main.go` — delegates login to `authd`; verifies via
  `auth.FetchKeys`; no local mint.
- `gated/ipc/...` (or `mcp-hostd` after the split) — registers
  `auth.MCPTools`; `mint_token` forwards to `authd`.

## Status (2026-05-26)

`auth/` exists as a Go package with `Authorize`, `VerifyJWT`,
`RequireSigned` / `StripUnsigned` middleware, OAuth handlers and account
linking. It currently verifies HS256 (shared `AUTH_SECRET`) — the launch
target is ES256 with JWKs, and `authd` does not yet exist as a daemon.
The Target shape API names (`Mint`, `MintNarrower`, `VerifyHTTP`,
`HasScope`, `MatchesAudience`, `Handlers`, `Mount`, `MCPTools`,
`FetchKeys`) are NOT yet exported under those names. `auth/web.go`,
`auth/oauth.go`, `auth/link.go`, `auth/authorize.go`, `auth/identity.go`
still import `core` / `store` / `theme`, so the package is not yet
reusable outside arizuko. Phase 1 (extract arizuko-specific code) and
Phase 2 (stand up `authd` + ES256) are outstanding — hence `partial`.

## Open

- **`LinkStore` backing.** SQLite at a configured path by default;
  options to point at postgres or share with another store via the
  `LinkStore` interface.
- **`mint_token` over MCP — is the agent the bearer or just a
  proxy?** The agent holds its own token (its identity). When it
  calls `mint_token`, `authd` uses the agent's scope as the parent for
  downscope and signs the narrower token. The minted token returns to
  the agent; the agent decides who to give it to (typically a
  sub-agent it spawns).
- **Wildcard scopes.** `*:*` allows everything. `tasks:*` allows any
  task verb. Match logic lives in `HasScope`; spec'd here as exact
  - namespace wildcards only. More complex matching later.
- **OAuth state cookie vs JWT.** Today the `state` parameter is a
  cookie. Library form keeps that; daemons can override via
  `StateStore`.

## Blueprint takeaway

The pattern this spec lands on — **central authority, distributed
verification**:

1. The thing that **signs** is a single daemon (`authd`). One private
   key, one issuer, one place to revoke and audit. Anything that mints
   trust is a singleton, never replicated across consumers.
2. The thing that **verifies** is a library every daemon imports.
   Verification is a pure function over `(token, public JWKs)` — no
   network hop, no fault dependency on `authd` being up.
3. `authd` publishes its public keys (JWKs at `/v1/keys`); consumers
   cache them and verify offline; `kid` drives rotation.

This is the first piece of the gated split and the blueprint for the
`<daemon>/api/v1/` + `types/` pattern the rest of the split adopts
later ([`U-genericization.md`](U-genericization.md) "gated split").
