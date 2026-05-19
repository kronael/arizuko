# auth

Identity, JWT, OAuth, authorization policy, HTTP middleware.

## Purpose

Shared auth primitives used across daemons. Four concerns: (1) user auth
(password argon2, JWT sessions, OAuth providers, Telegram widget),
(2) runtime identity resolution for agents (`Identity` from folder and
tier), (3) tool-level authorization via `Authorize`, (4) **the canonical
platform-token format** — every issuer mints through this library, every
verifier validates through this library. No daemon implements its own
JWT format.

## Platform token (per `specs/6/R-platform-api.md`)

Single signed JWT shape for all federated `/v1/*` calls and the future
agent capability token. HS256, signed with `AUTH_SECRET`:

```json
{
  "sub":    "user:abc123" | "agent:atlas/main" | "key:k_42",
  "scope":  ["groups:read", "tasks:write", "messages:send", "*:read"],
  "folder": "atlas/main",
  "tier":   2,
  "iat":    1735000000,
  "exp":    1735003600,
  "iss":    "proxyd" | "mcp-host" | "onbod"
}
```

Scopes are `<resource>:<verb>` pairs; `*:*` is root, `tasks:*` matches
all task verbs. `folder` scopes the token to a subtree (`atlas/main`
token cannot touch `rhias/*`). `tier` is denormalized from grants for
fast tier-gated checks. Scopes are minted from grant rules at issuance
time (snapshot) — revocation is by short TTL until a revocation list
becomes load-bearing.

**Permitted issuers** (each calls `auth.Mint(...)`, produces the same
JWT shape, differs only in scope breadth):

- `proxyd` — OAuth login, user session token
- `mcp-host` — currently `ipc/` inside gated; mints agent capability
  token at socket bind, embedding `(folder, tier, grants snapshot)`
- `onbod` — invite redemption / admission, narrow initial-session scope
- `dashd` (planned) — long-lived API keys for operator automation

## Public API

**Status quo (shipped).**

- Identity: `Identity`, `Resolve(folder string) Identity`, `WorldOf(folder)`, `IsDirectChild(parent, child)`, `CheckSpawnAllowed`
- Authorization: `Authorize(id Identity, tool string, target AuthzTarget) error`, `AuthzTarget`, `MatchGroups(allowed, folder)`
- Session JWT (today's shape: `sub, name, groups`): `Claims`, `VerifyJWT(secret, token)`
- OAuth: GitHub, Google, Discord, Telegram widget — shared `createOAuthSession`
- HMAC: `SignHMAC`, `VerifyHMAC`, `UserSigMessage`, `SlinkSigMessage`, `VerifyUserSig`, `VerifySlinkSig`
- Password: `HashToken`, argon2 verify
- Middleware: `RegisterRoutes(mux, store, cfg)` mounts `/auth/*`;
  `RequireSigned(secret)` / `StripUnsigned(secret)` for proxyd-signed
  identity headers (the pre-`/v1/*` trust mechanism)

**Planned (per `specs/6/R-platform-api.md` §"Token model").** Not yet
implemented; this README is the contract:

- `Mint(secret []byte, c Claims) (string, error)` — single mint entry
  for every issuer (proxyd, mcp-host, onbod, dashd). Sets `iat`/`exp`,
  signs HS256.
- `VerifyHTTP(r *http.Request) (Identity, error)` — extracts
  `Authorization: Bearer <jwt>`, verifies signature + `exp` + `iss`,
  returns `Identity{sub, scope, folder, tier}`.
- `HasScope(id Identity, resource, verb string) bool` — wildcard-aware
  scope check (`*:*`, `tasks:*`, `*:read`).
- `MatchesFolder(id Identity, target string) bool` — subtree match;
  `folder == "*"` or unset = root.

Per-request shape every `/v1/*` handler will use:

```go
ident, err := auth.VerifyHTTP(r)
if err != nil                                   { return 401 }
if !auth.HasScope(ident, "tasks", "write")      { return 403 }
if !auth.MatchesFolder(ident, taskFolder)       { return 403 }
```

## Dependencies

- `core`, `store`

## Configuration

- `AUTH_SECRET` — HS256 signing key for **every** platform token
  (sessions, agent caps, onbod-issued, dashd-issued). Single source;
  rotating it invalidates all tokens.
- `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GITHUB_ALLOWED_ORG`
- `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_ALLOWED_EMAILS`
- `DISCORD_CLIENT_ID/SECRET`
- `TELEGRAM_BOT_TOKEN` (widget verification)

## Files

- `identity.go` — tier/world resolution, spawn rules
- `policy.go` — `Authorize` per tool
- `acl.go` — `MatchGroups`, glob ACL
- `jwt.go`, `middleware.go`, `web.go` — session handling + login routes
- `oauth.go` — provider dance
- `hmac.go` — inter-daemon header signing

## Related docs

- `ARCHITECTURE.md` (Auth Hardening)
- `specs/5/A-auth-consolidated.md`, `specs/6/9-acl-unified.md`
- `specs/6/R-platform-api.md` — full token contract; auth/ is the
  single source of truth for the JWT shape every federated daemon
  consumes
