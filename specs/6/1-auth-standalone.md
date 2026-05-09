---
status: spec
---

# auth: standalone token authority

Make `auth/` a fully generic token-authority component: a tight Go
library + a thin `authd` HTTP daemon + a small MCP tool surface.
Anyone — arizuko or not — should be able to drop it in to mint and
verify capability tokens, run an OAuth login flow, or expose
self-service identity to agents and operators.

This is the **first blueprint** for genericization. The shape this
spec lands on (library + daemon + MCP) is the pattern proxyd, timed,
and the routerd extraction will follow.

## Why auth first

- The smallest reachable cut: most of the code is already generic
  (`hmac.go`, `jwt.go`, `oauth.go`, `middleware.go`, `web.go`); only
  three files carry arizuko-specific concepts (`acl.go`, `policy.go`,
  `identity.go`).
- Every other daemon depends on this contract. A clean auth library
  is the prerequisite for any of them being deployable in isolation
  (every daemon has to verify tokens; every issuer has to mint them).
- A standalone `authd` daemon also unblocks non-Go consumers of
  arizuko's token format — language doesn't matter once HTTP is the
  contract.

## Today's surface

```
auth/
  hmac.go             generic — HMAC sign/verify of identity headers
  jwt.go              generic — JWT structure (planned mint API)
  oauth.go            generic — OAuth provider abstraction
  middleware.go       generic — RequireSigned / StripUnsigned guards
  web.go              generic — login/callback/logout HTTP handlers
  routes.go           generic — provider route registration
  link.go             generic — account linking (multi-provider one user)
  collide.go          generic — handles two providers landing on same user
  ───
  acl.go              arizuko — folder-tier ACL evaluator
  policy.go           arizuko — policy composition for arizuko grants
  identity.go         arizuko — identity model with folder/tier fields
```

`acl.go`, `policy.go`, `identity.go` will move out (see "Target
shape" below). The rest stays in `auth/`.

## Target shape

Three pieces:

### 1. `auth/` library — pure mint/verify primitives

After cleanup, `auth/` is platform-neutral. It exposes:

```go
// Mint a capability token. Caller composes the claims.
func Mint(claims Claims, key []byte, ttl time.Duration) (string, error)

// Verify any incoming token. Returns Identity if valid.
func VerifyHTTP(r *http.Request, key []byte) (Identity, error)
func VerifyToken(token string, key []byte) (Identity, error)

// Scope check primitives.
func HasScope(ident Identity, resource, verb string) bool

// OAuth flow.
func RegisterRoutes(mux *http.ServeMux, providers []OAuthProvider, key []byte, opts ...Option)
```

`Claims` and `Identity` carry only generic fields:

```go
type Claims struct {
    Sub      string                 // subject (user, agent, key)
    Scope    []string               // capability list ("tasks:write", ...)
    Audience string                 // optional, for multi-app deployments
    Extra    map[string]string      // app-specific opaque fields
    TTL      time.Duration
    Issuer   string                 // "proxyd", "authd", "mcp-host", ...
}

type Identity struct {
    Sub      string
    Scope    []string
    Audience string
    Extra    map[string]string      // includes "folder", "tier" for arizuko consumers
    Issuer   string
    Expires  time.Time
}
```

Folder, tier, and other arizuko concepts move into `Extra` — the
library doesn't know what those keys mean. Arizuko-specific helpers
(folder-match, tier-int) live in a new `arizuko/identity.go` outside
the library, building on the generic `Identity`.

### 2. `authd/` daemon — HTTP wrapper

A minimal Go binary mounting:

```
GET   /auth/login?provider=…&redirect=…
GET   /auth/callback                       # OAuth code exchange → token cookie
POST  /auth/logout
GET   /auth/me                             # current identity (verifies cookie/Bearer)
POST  /v1/tokens                           # mint (requires admin scope)
POST  /v1/tokens:verify                    # introspect a token
POST  /v1/tokens:revoke                    # add to revocation list (planned)
GET   /v1/providers                        # list configured OAuth providers
GET   /v1/users/{sub}                      # identity record (multi-provider linking)
GET   /health
```

Configured via TOML:

```toml
listen     = ":8080"
secret_env = "AUTH_SECRET"
ttl        = "1h"

[[provider]]
id      = "google"
type    = "oidc"
client_id = "..."
client_secret_env = "GOOGLE_OAUTH_SECRET"
discovery_url = "https://accounts.google.com/.well-known/openid-configuration"

[[provider]]
id   = "github"
type = "github"
# ...
```

Stateless except for: pending OAuth state (short TTL in-memory or
redis-shaped), account-linking records (sqlite or postgres). No
arizuko schema dependency.

### 3. MCP tool surface

For agents that need to introspect or mint sub-tokens (e.g. a
delegation pattern where one agent issues a narrower-scope token to
a sub-agent it spawns):

| Tool             | Purpose                                        | Scope required     |
| ---------------- | ---------------------------------------------- | ------------------ |
| `whoami`         | Return the agent's own Identity                | none               |
| `mint_token`     | Mint a token narrower than caller's own scope  | `tokens:mint:self` |
| `verify_token`   | Introspect a token (does it parse, what scope) | `tokens:read`      |
| `list_providers` | List configured OAuth providers                | none               |
| `revoke_token`   | Add token to revocation list (when shipped)    | `tokens:revoke`    |

`mint_token` is the interesting one — it enforces "caller cannot
mint scopes broader than its own", so an agent can downscope itself
or a subagent without escalation. Useful once multi-agent flows
land.

The MCP tools are exposed by `authd` as a built-in MCP server (see
"MCP integration pattern" below); other daemons that need agent-side
auth pull tools by importing this same MCP server registration.

## Outward API contract — full

```
GET  /auth/login?provider=<id>&redirect=<url>
     302 → provider OAuth URL with state cookie

GET  /auth/callback?code=…&state=…
     OAuth code exchange. On success: 302 to redirect with
     Set-Cookie: session=<jwt>. Errors return 4xx with body.

POST /auth/logout
     Clears cookie. 200.

GET  /auth/me
     Authorization: Bearer <token>  OR  Cookie: session=<token>
     200 { sub, scope, audience, extra, issuer, expires }
     401 if missing/invalid.

POST /v1/tokens
     Authorization: Bearer <admin-token>
     { sub, scope[], audience?, extra?, ttl_seconds? }
     200 { token, expires }
     403 if caller lacks scope `tokens:mint`.
     400 if requested scope > caller's scope (downscope only).

POST /v1/tokens:verify
     Authorization: Bearer <any valid token>  (introspection is open
     to anyone holding any valid token — they're proving they're
     known)
     { token: "..." }
     200 { valid: true|false, identity: {...}, error: "..." }

GET  /v1/providers
     200 [{ id, type, label }]
     Public — used by login UIs.

GET  /v1/users/{sub}
     Authorization: Bearer <token>  (caller must be sub or admin)
     200 { sub, name, providers: [{provider, sub_in_provider, linked_at}] }
     404 if unknown.

GET  /health
     200 ok
```

Errors uniform across `/v1/*`:

```json
{ "error": { "code": "forbidden", "message": "scope tokens:mint required" } }
```

## Token format

Standard JWT (HS256) with `AUTH_SECRET`:

```json
{
  "sub": "user:abc123",
  "scope": ["tasks:write", "messages:read"],
  "aud": "arizuko",
  "extra": { "folder": "atlas/main", "tier": "2" },
  "iat": 1735000000,
  "exp": 1735003600,
  "iss": "proxyd"
}
```

`extra` is opaque to `auth/`; arizuko consumers read `folder` /
`tier` from it. Other deployments ignore the field.

## What changes from today

| Today                                                                  | After                                                                   |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------------- |
| `auth/` mixes generic + arizuko code                                   | `auth/` is pure generic; `arizuko/identity.go` adds arizuko helpers     |
| Token mint scattered (proxyd writes its own; `auth.Mint` planned only) | One canonical `auth.Mint` with downscope-only enforcement at HTTP layer |
| Identity headers signed via HMAC, JWTs separately                      | JWT is the wire format; HMAC headers are a transition mechanism only    |
| No HTTP API for token operations                                       | `authd` exposes `/v1/tokens/*` for non-Go consumers                     |
| No agent-facing token tools                                            | MCP tools (`whoami`, `mint_token`, …) for delegation flows              |
| OAuth provider list hardcoded in proxyd                                | TOML-configured in `authd`                                              |

## What this spec is not

- Not centralized revocation. A revocation list is sketched but
  parked — short TTL is the default mitigation. If revocation
  becomes load-bearing, that's its own spec.
- Not OIDC discovery beyond what's needed for login. Full OIDC
  conformance is later work.
- Not multi-tenancy at the auth layer. `audience` exists for that
  but enforcement (one signing key per audience) is deferred.
- Not a database split. `authd`'s state (account linking) lives
  wherever the operator points it; today that's the same SQLite as
  the rest. Per-daemon DB is a separate phase.

## Implementation phases

1. **Extract arizuko-specific code** — move `acl.go`, `policy.go`,
   `identity.go` arizuko fields into a new `arizuko/identity.go` (or
   similar). `auth/` left with only generic primitives. No
   behaviour change. Pure refactor.

2. **Implement `auth.Mint`** — the function is referenced in the
   API spec but not yet implemented. Land it; migrate proxyd's
   existing JWT issuance to use it. Backward compatible: same JWT
   format, same `AUTH_SECRET`.

3. **Stand up `authd`** — new binary at `authd/main.go`. Mount the
   HTTP API listed above. Initially serves a deployment with
   identical OAuth config to today's proxyd; can be deployed
   side-by-side.

4. **MCP tools** — add `whoami`, `mint_token`, `verify_token`,
   `list_providers` to a built-in MCP server in `authd`. Expose via
   unix socket or HTTP-MCP endpoint.

5. **Migrate proxyd** — proxyd stops minting tokens directly; it
   calls `authd /v1/tokens` (or imports the library form). proxyd
   becomes pure gateway + proxy.

6. **Document for non-arizuko deployment** — `authd/README.md`
   covers "drop me in next to your service" usage. Example configs
   for Google/GitHub/OIDC providers.

After step 2 the library is clean. After step 3 standalone deployment
is possible. After step 5 the arizuko stack uses authd as the canonical
issuer.

## Code pointers

- `auth/hmac.go`, `auth/jwt.go`, `auth/oauth.go` — keep as-is, generic.
- `auth/middleware.go`, `auth/web.go`, `auth/routes.go`, `auth/link.go`,
  `auth/collide.go` — keep, generic.
- `auth/acl.go`, `auth/policy.go`, `auth/identity.go` — move out
  (folder/tier-aware code).
- `proxyd/main.go` — JWT mint moves to call `auth.Mint` (or `authd`).
- `arizuko/identity.go` (new) — folder/tier helpers reading from
  `Identity.Extra`.
- `authd/main.go` (new) — daemon entry; mounts HTTP API + MCP tools.
- `authd/README.md` (new) — usage as standalone component.

## Open

- **Where does `authd` keep linking state?** Defaults to SQLite next
  to its config. Operators wanting central state (e.g. one authd for
  many backends) can point at a shared file or postgres. Schema is
  small (one table + linked-providers join).
- **How does `mint_token` enforce downscope?** Caller's scope set
  must be a superset of requested scope set. `*:*` allows anything.
  Wildcards (`tasks:*`) match anything in the namespace. Easy to
  implement; needs care on operator-issued tokens.
- **Does `authd` host the MCP server itself, or expose it via webd
  for browser-side agents?** Lean: authd hosts directly over a unix
  socket; webd proxies if a browser-side path is needed.
- **Does the library form survive long-term, or do all consumers
  call `authd` over HTTP?** Lean: both — Go consumers can call
  library or daemon; non-Go must use daemon. The library is just
  the daemon's handler logic with the HTTP layer stripped.
