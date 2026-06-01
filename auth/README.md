# auth

Identity, ES256 tokens, JWKS, OAuth, authorization policy, HTTP middleware.

## Purpose

Shared auth primitives used across daemons. Four concerns: (1) user auth
(password argon2, JWT sessions, OAuth providers, Telegram widget),
(2) runtime identity resolution for agents (`Identity` from folder and
tier), (3) authorization — a structural gate (`AuthorizeStructural`) and
an ACL row-grant gate (`Authorize`), (4) **the canonical platform-token
format** — authd signs, every backend verifies through this library. No
daemon implements its own JWT format.

## Platform token (per `specs/5/5-uniform-mcp-rest.md`, `specs/5/1`)

ES256 signed JWT for all federated `/v1/*` calls and agent capability
tokens. authd is the **sole signer**; backends only verify against the
cached public JWKS — they never link the signing path. `TokenClaims`
(`es256.go`) is the payload:

```json
{
  "sub":        "user:abc123" | "service:routd" | "agent:atlas/main",
  "typ":        "user" | "service" | "downscoped",
  "scope":      ["groups:read", "tasks:write", "messages:send"],
  "aud":        "atlas",
  "iss":        "authd",
  "iat":        1735000000,
  "nbf":        1735000000,
  "exp":        1735003600,
  "jti":        "<128-bit base64url>",
  "parent_jti": "<parent token jti, downscoped only>"
}
```

`Extra` (`map[string]string`) round-trips as top-level claims, not a
nested object — e.g. `arz/folder` is read back as `Subject.Extra["arz/folder"]`.
Scopes are `<resource>:<verb>` pairs; wildcards are namespace-only
(`tasks:*`), there is no global `*:*` (operators carry the enumerated
resource list).

**Signing** (`SigningKey`, ES256 P-256 keypair; `Kid` rides the JWS
header for rotation):

- `(*SigningKey).Sign(c TokenClaims, ttl) (string, error)` — low-level
  mint; forces `iss=authd`, sets `iat/nbf/exp`, fills `jti`.
- `(*SigningKey).MintForSubject(targetGrants []string, c, ttl)` —
  authoritative issuance after OAuth/service-key exchange; granted scope
  = requested ∩ targetGrants (all of targetGrants if requested empty).
- `(*SigningKey).MintNarrower(parentScope []string, c, ttl)` — delegated
  downscope (backs `mint_token` MCP tool); requested scope must be a
  subset of parent's, else `ErrScopeTooBroad`. Sets `typ=downscoped`.

## Public API

- Identity: `Identity`, `Resolve(folder string) Identity`, `WorldOf(folder)`, `IsDirectChild(parent, child)`, `CheckSpawnAllowed`
- Structural authz: `AuthorizeStructural(id Identity, tool string, target AuthzTarget) error`, `AuthzTarget`, `MatchGroups(allowed, folder)` — tree-shape invariants (caller-folder prefix, tier bounds, task-owner-must-match).
- ACL row-grant authz: `Authorize(s *store.Store, caller Caller, action, scope string, params map[string]string) bool` and `AuthorizeWith(..., opts AuthorizeOpts)` — deny-wins row grants with tier-default fallback for `mcp:*` actions. Many tool callsites need both gates.
- Token verify: `VerifyToken(token string, ks *KeySet) (Subject, error)`, `VerifyHTTP(r *http.Request, ks *KeySet) (Subject, error)`, `HasScope(scope []string, resource, verb string) bool`, `MatchesAudience(sub Subject, aud)`.
- JWKS: `PublicJWKS(keys ...*SigningKey) ([]byte, error)` (authd's `/v1/keys`); `FetchKeys(ctx, authdURL)` + `KeySet` (backend-side cache).
- Service bootstrap: `ServiceToken(authdURL, daemon, key) (*TokenSource, error)`; `(*TokenSource).Token(ctx)` returns a live, auto-refreshed `service:<name>` token.
- Session JWT (legacy HS256, `sub, name, groups`): `Claims`, `VerifyJWT(secret, token)`.
- OAuth: GitHub, Google, Discord, Telegram widget — shared `createOAuthSession`.
- HMAC: `SignHMAC`, `VerifyHMAC`, `UserSigMessage`, `ChatSigMessage`, `VerifyUserSig`, `VerifyChatSig`.
- Password: `HashToken`, argon2 verify.
- Middleware: `RegisterRoutes(mux, store, cfg)` mounts `/auth/*`;
  `RequireSigned(secret)` / `StripUnsigned(secret)` for proxyd-signed
  identity headers; `RequireSignedOrBearer(secret, ks)` /
  `StripUnsignedOrBearer(secret, ks)` accept either a proxyd signature
  or a Bearer ES256 token.

Per-request shape on a `/v1/*` handler:

```go
sub, err := auth.VerifyHTTP(r, ks)
if err != nil                              { return 401 }
if !auth.HasScope(sub.Scope, "tasks", "write") { return 403 }
```

## Dependencies

- `core`, `store`

## Configuration

- `AUTH_SECRET` — HMAC secret for legacy session JWTs + proxyd-signed
  identity headers. ES256 platform tokens are signed by authd's keypair,
  not this secret.
- `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GITHUB_ALLOWED_ORG`
- `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_ALLOWED_EMAILS`
- `DISCORD_CLIENT_ID/SECRET`
- `TELEGRAM_BOT_TOKEN` (widget verification)

## Files

- `identity.go` — tier/world resolution, spawn rules
- `policy.go` — `AuthorizeStructural` (tree-shape / tier-bound gate)
- `authorize.go` — `Authorize`/`AuthorizeWith` (ACL row-grant gate, `Caller`)
- `es256.go` — `TokenClaims`, `Subject`, `SigningKey`, `Sign`/`MintForSubject`/`MintNarrower`
- `jwks.go` — `KeySet`, `VerifyToken`, `VerifyHTTP`, `PublicJWKS`, `FetchKeys`
- `scope.go` — `HasScope`, scope intersection/coverage helpers
- `service.go` — daemon service-token bootstrap (`ServiceToken`, `TokenSource`)
- `acl.go` — `MatchGroups`, glob ACL
- `routes.go` — `RegisterRoutes` mounts `/auth/*`
- `link.go` — account link-code redemption; `collide.go` — sub-collision resolution UI
- `jwt.go`, `middleware.go`, `web.go` — legacy session handling + login routes
- `oauth.go` — provider dance
- `hmac.go` — inter-daemon header signing

## Related docs

- `ARCHITECTURE.md` (Auth Hardening)
- `specs/4/9-acl-unified.md` — canonical ACL
- `specs/5/5-uniform-mcp-rest.md` — full token contract; auth/ is the
  single source of truth for the JWT shape every federated daemon
  consumes
