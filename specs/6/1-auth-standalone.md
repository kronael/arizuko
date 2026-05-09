---
status: spec
---

# auth: capability library

`auth/` is a Go capability library — not a daemon. Mint, verify,
scope-check, OAuth flow, and MCP tool handlers all live in the
library. Any daemon that needs auth imports the library and mounts
the handlers it wants on its own HTTP mux and MCP server. No
network hop for verification. No `authd` binary.

This is the **first blueprint** for genericization. It establishes a
pattern other components will mirror only where it fits: many
components (timed, proxyd, routerd) genuinely need a daemon because
they own long-running state or network protocols. Auth doesn't —
it's pure functions + handlers — so it ships as a library.

## Why no daemon

A daemon makes sense when there's something the library can't do
in-process:

- Long-running state that only one process should own (a cron
  scheduler, a message router, a gateway holding sessions).
- A network protocol surface external clients must hit (HTTP,
  gRPC, MCP socket).
- Hardware or kernel resources (a sandbox supervisor, a TLS
  terminator).

Auth has none of those. Verification is a function over `(token,
key)`. Mint is a function over `(claims, key)`. Scope check is a
predicate. OAuth flow needs HTTP handlers, but those handlers are
stateless or use short-TTL pending-state — they can mount on any
daemon's existing HTTP mux. MCP tools are tiny and can be
registered with any daemon's MCP server.

Adding a daemon would only add latency (network hop per
verification) and a fault domain (every backend now depends on auth
being up). For zero benefit.

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
  acl.go              arizuko — folder-tier ACL evaluator
  policy.go           arizuko — policy composition for arizuko grants
  identity.go         arizuko — identity model with folder/tier fields
```

`acl.go`, `policy.go`, `identity.go` move out (see Target shape).
The rest stays.

## Target shape

A single Go module exposing four surfaces:

### 1. Verification primitives

```go
// Verify any incoming token. Pure function, no IO.
func VerifyHTTP(r *http.Request, key []byte) (Identity, error)
func VerifyToken(token string, key []byte) (Identity, error)

// Scope check primitives.
func HasScope(ident Identity, resource, verb string) bool
func MatchesAudience(ident Identity, aud string) bool
```

### 2. Mint primitives

```go
// Mint a capability token. Caller composes claims + holds the key.
func Mint(claims Claims, key []byte, ttl time.Duration) (string, error)

// Downscope: mint a narrower token from an existing one. Used for
// agent → sub-agent delegation, and for the `mint_token` MCP tool.
// Returns error if requested scope is broader than parent's scope.
func MintNarrower(parent Identity, claims Claims, key []byte, ttl time.Duration) (string, error)
```

`Claims` and `Identity` carry only generic fields:

```go
type Claims struct {
    Sub      string                 // subject (user, agent, key)
    Scope    []string               // capability list ("tasks:write", ...)
    Audience string                 // optional, multi-app deployments
    Extra    map[string]string      // app-specific opaque fields
    TTL      time.Duration
    Issuer   string                 // "proxyd", "mcp-host", ...
}

type Identity struct {
    Sub      string
    Scope    []string
    Audience string
    Extra    map[string]string      // includes "folder", "tier" for arizuko
    Issuer   string
    Expires  time.Time
}
```

Folder, tier, and other arizuko concepts move into `Extra`. The
library doesn't know what those keys mean. Arizuko-specific helpers
live in `arizuko/identity.go` outside `auth/`, building on the
generic `Identity`.

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

Today proxyd is the daemon that mounts these. Tomorrow any daemon
can — multiple deployments, multiple gateways, all using the same
handlers from the same library. No `authd` binary mediating.

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
func MCPTools(key []byte) []MCPTool

// MCPTool is a small struct: name, description, schema, handler.
// Hosting daemon iterates and registers each with its server.
```

| Tool             | Purpose                                        | Scope required            |
| ---------------- | ---------------------------------------------- | ------------------------- |
| `whoami`         | Return the caller's own Identity               | none                      |
| `mint_token`     | Mint a token narrower than caller's own scope  | (downscope-only enforced) |
| `verify_token`   | Introspect a token (does it parse, what scope) | any valid token           |
| `list_providers` | List configured OAuth providers                | none                      |

`mint_token` enforces downscope-only via `MintNarrower`: the agent
can only issue tokens with a strict subset of its own scope. This is
the primitive that makes agent → sub-agent delegation safe without
admin in the loop.

The MCP host (gated's ipc subsystem today) mounts these alongside its
existing tools. Each agent's MCP socket then carries the auth tools
locally — same library, in-process, no network hop.

## Mounting pattern

A daemon that wants the OAuth flow + MCP tools writes:

```go
import "github.com/onvos/arizuko/auth"

func main() {
    key := []byte(os.Getenv("AUTH_SECRET"))

    // OAuth handlers on the HTTP mux.
    mux := http.NewServeMux()
    auth.Mount(mux, providers, key,
        auth.WithLinkStore(linkStore),
        auth.WithStateStore(stateStore),
    )

    // MCP tools on the MCP server (if this daemon hosts one).
    for _, tool := range auth.MCPTools(key) {
        mcpServer.RegisterTool(tool)
    }

    // ... daemon's own routes and tools ...

    http.ListenAndServe(":8080", mux)
}
```

Three lines per daemon to add full auth, no service dependency.

## Where each role lives, after this lands

| Role                                  | Where                                                   |
| ------------------------------------- | ------------------------------------------------------- |
| Verify a token                        | Anywhere — call `auth.VerifyHTTP` in-process            |
| Mint a token from claims              | Wherever the signing key is (proxyd, MCP host, …)       |
| Downscope an existing token           | `auth.MintNarrower` in-process                          |
| OAuth login + callback                | proxyd today (mounts `auth.Handlers`); any daemon could |
| MCP tools (`whoami`, `mint_token`, …) | gated's ipc subsystem (mounts `auth.MCPTools`)          |
| Account-linking storage               | Pluggable; default SQLite via `LinkStore`               |
| Pending OAuth state                   | Pluggable; default in-memory via `StateStore`           |

## What this spec is not

- Not an auth daemon. The library is the entire shipping artifact;
  daemons import it.
- Not centralized revocation. Short TTL is the default; a
  revocation list is a future addition (a `RevocationStore`
  interface; the library checks it during `Verify`).
- Not full OIDC conformance. Login flow is JWT-issuing OAuth code
  exchange — enough for arizuko + most deployments.
- Not multi-key per audience. One `AUTH_SECRET` per deployment.
  Audience is for routing, not key separation.

## Implementation phases

1. **Extract arizuko-specific code.** Move `acl.go`, `policy.go`,
   `identity.go` (folder/tier fields) out of `auth/` into a new
   `arizuko/identity.go`. `auth/` left with only generic primitives.
   Pure refactor.

2. **Implement `Mint` + `MintNarrower`.** The functions are
   referenced in the API but not yet coded. Land them; migrate
   proxyd's existing JWT-mint to call `auth.Mint`.

3. **Refactor OAuth handlers as mountable.** `auth.Handlers` /
   `auth.Mount` returns/registers the four routes. Pluggable
   `StateStore` and `LinkStore` interfaces with default
   implementations.

4. **MCP tool handlers.** `auth.MCPTools` returns the four tools
   ready to register. gated's ipc subsystem (today's MCP host)
   mounts them.

5. **Document for non-arizuko deployment.** `auth/README.md` covers
   "drop me in your service" usage. Examples for OAuth providers
   (Google/GitHub/OIDC), account linking, MCP integration.

After step 2 the library is internally consistent. After step 4 the
full target surface is shipped. No new binaries; no new processes.

## Code pointers

- `auth/hmac.go`, `auth/jwt.go`, `auth/oauth.go`, `auth/middleware.go`,
  `auth/web.go`, `auth/routes.go`, `auth/link.go`, `auth/collide.go` —
  keep, generic.
- `auth/acl.go`, `auth/policy.go`, `auth/identity.go` — move out
  to `arizuko/identity.go` (or similar).
- `auth/mcp.go` (new) — MCP tool definitions returned by `MCPTools`.
- `auth/store.go` (new) — `StateStore` and `LinkStore` interfaces +
  default implementations.
- `arizuko/identity.go` (new) — folder/tier helpers reading from
  `Identity.Extra`.
- `proxyd/main.go` — calls `auth.Mount` for OAuth, `auth.Mint` for
  tokens.
- `gated/ipc/...` (or wherever the MCP host lives) — registers
  `auth.MCPTools` alongside its own tools.

## Open

- **Where do account-linking records live by default?** SQLite at a
  configured path. Options to point at postgres or share with another
  store via `LinkStore` interface.
- **`mint_token` over MCP — is the agent the bearer or just a
  proxy?** The agent holds its own token (its identity). When it
  calls `mint_token`, the library uses the agent's scope as the
  parent for downscope. The minted token returns to the agent; the
  agent decides who to give it to (typically a sub-agent it
  spawns).
- **Wildcard scopes.** `*:*` allows everything. `tasks:*` allows any
  task verb. Match logic lives in `HasScope`; spec'd here as exact
  - namespace wildcards only. More complex matching later.
- **OAuth state cookie vs JWT.** Today the `state` parameter is a
  cookie. Library form keeps that; daemons can override via
  `StateStore`.

## Blueprint takeaway

The pattern this spec lands on:

1. Decide if the component genuinely needs a daemon. (Long-running
   state? Network protocol? Shared resource?) If no — make it a
   library.
2. Export verification primitives, mint primitives (if it issues
   anything), HTTP handlers (if it needs flows), and MCP handlers
   (if agents need to interact). All as functions/structs the
   consuming daemon mounts.
3. Daemons import and mount what they need.

Auth fits this. proxyd, timed, routerd don't — they're genuinely
daemons. Their specs follow a different blueprint (library + daemon

- MCP) because they own state and protocols.
