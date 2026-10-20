# auth

Identity, ES256 tokens, JWKS, OAuth, authorization policy, HTTP middleware.

## Purpose

Shared auth primitives used across daemons. Four concerns:

1. **The canonical platform-token format.** authd signs, every backend
   verifies through this library. No daemon implements its own JWT format.
2. **One authorization evaluator.** `Authorize` matches ACL rows over the
   caller's expanded principal set, deny-wins, and returns false when nothing
   matches. Both the operator REST face and the agent socket reach it; what
   differs is the identity source and the target bound, never the decision.
3. **Identity resolution.** `Identity` is a folder's non-elevated authz
   coordinate. The path carries ZERO authorization — only its own name and the
   root predicate (5/33 decision 2).
4. **The OAuth exchange half.** Pure exchange/userinfo wrappers plus state,
   PKCE and cookie helpers; authd owns every route.

## Platform token (per `specs/5/17-openapi-mcp.md`, `specs/5/1`)

ES256 signed JWT for every federated `/v1/*` call. authd is the **sole
signer**; backends only verify against the cached public JWKS — they never
link the signing path. The agent socket carries no token at all: its identity
is the unix socket routd bound for the turn (`ipc/README.md`). `TokenClaims`
(`es256.go`) is the payload:

```json
{
  "sub":        "user:abc123" | "service:routd",
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

- Identity: `Identity`, `Resolve(folder)`, `BareSub`, `FolderPrincipal`,
  `CallerGroups`, `IsOperator`
- Containment: `MatchGroups(allowed, folder)` — the pattern IS the
  containment. `atlas` covers exactly `atlas`, `atlas/*` its direct children,
  `atlas/**` the whole subtree, `**` everything. A parent scope does NOT reach
  a child by position.
- Web slots: `MatchSlot(allowed, slotPath)` gates `/priv/<path>`. It admits
  every path below a prefix a scope covers, because slot trees nest on disk:
  folder `atlas`'s `~/private_html` holds `atlas/search`'s slot. That is
  filesystem containment for web slots only, never a folder decision.
- Authorization: `Authorize(store, caller, action, scope, params) bool` —
  deny-wins row grants, no fallback. `EffectiveActions` is the `tools/list`
  visibility view over the same rows, so the listing and the gate agree.
  `UserScopes` projects a sub's scopes (it ignores the action, so the gate
  must not). `Delegate` bounds re-delegation to a subset of rows held WITH
  GRANT OPTION. `ShellEnvKeys` answers which `shell:env:<KEY>` secrets a
  folder may take into its shell. `CheckHold` decides whether a call is held
  for human approval (spec 5/19). `ValidateParams` / `ParamsSpec` are
  the param-glob half.
- Token verify: `VerifyToken(token, ks)`, `VerifyHTTP(r, ks)`,
  `HasScope(scope, resource, verb)`, `HasScopeCoveredBy`.
- JWKS: `PublicJWKS(keys…)` (authd's `/v1/keys`); `FetchKeys(ctx, authdURL)`,
  `NewKeySet`, `NewKeySetWithRetirement` (backend-side cache).
- Service bootstrap: `ServiceToken(authdURL, daemon, key) (*TokenSource, error)`;
  `(*TokenSource).Token(ctx)` returns a live, auto-refreshed `service:<name>`
  token.
- Dashboard gate: `RequireOperator`, `RequireWrite`, `Forbidden` — the ONE
  operator gate every per-daemon dashboard applies (spec 6/28). It was three
  copies that had already drifted into answering differently per daemon.
- CSRF: `EnsureCSRF`, `CheckCSRF`, `CSRFField` — double-submit for
  cookie-authenticated HTML forms. One implementation, because a second would
  drift.
- OAuth wrappers (authd drives `/auth/*`, this lib has no mount layer):
  `ExchangeGitHub`/`FetchGitHubUser`, `ExchangeGoogle`/`FetchGoogleUser`,
  `ExchangeDiscord`/`FetchDiscordUser`, `VerifyTelegramWidget`,
  `SignState`/`VerifyState`/`StateIntent`, `WritePKCE`/`ConsumePKCE`,
  `WriteStateCookie`, `SafeReturn`, `JSSafe`, `MatchEmailAllowlist`,
  `CheckGitHubOrgMember`, `AuthBaseURL`.
- Middleware: `ProxydTransit(r, ks)` — reports whether the request carries
  a valid `service:proxyd` ES256 bearer, the transit proof that lets a
  backend trust proxyd's stamped `X-User-*` headers. `ks==nil` (local dev,
  `AUTHD_URL` unset) → false.

Per-request shape on a `/v1/*` handler:

```go
sub, err := auth.VerifyHTTP(r, ks)
if err != nil                              { return 401 }
if !auth.HasScope(sub.Scope, "tasks", "write") { return 403 }
```

## Dependencies

- `core`, `store`

## Configuration

- `AUTH_SECRET` — read by authd, proxyd and dashd, not by this package. It
  keys the OAuth CSRF state and is proxyd's auth-enabled flag. Platform tokens
  are ES256, signed by authd's keypair: one leaked shared secret must not be
  enough to forge an identity (BUGS `L6`).
- `AUTH_BASE_URL`
- `GITHUB_CLIENT_ID/SECRET`, `GITHUB_ALLOWED_ORG`
- `GOOGLE_CLIENT_ID/SECRET`, `GOOGLE_ALLOWED_EMAILS`
- `DISCORD_CLIENT_ID/SECRET`
- `TELEGRAM_BOT_TOKEN` (widget verification)

## Files

- `identity.go` — `Identity` and `Resolve`: a folder's own name plus the root predicate, and nothing else
- `acl.go` — `MatchGroups`, scope-glob matching
- `authorize.go` — `Authorize`, `EffectiveActions` (ACL row-grant gate, `Caller`)
- `delegate.go` — `Delegate` (subset-of-held, WITH GRANT OPTION)
- `es256.go` — `TokenClaims`, `Subject`, `SigningKey`, `Sign`/`MintForSubject`/`MintNarrower`
- `jwks.go` — `KeySet`, `VerifyToken`, `VerifyHTTP`, `PublicJWKS`, `FetchKeys`
- `scope.go` — `HasScope` and the coverage helpers
- `service.go` — daemon service-token bootstrap (`ServiceToken`, `TokenSource`)
- `sub.go` — `BareSub`. The `user:`/`service:` prefix exists ONLY in the JWT
  sub claim; every stored principal is bare, and a prefixed subject compared
  against stored rows matches nothing and silently grants nothing
- `dashauth.go` — `RequireOperator`/`RequireWrite`/`Forbidden`, the one
  dashboard gate
- `csrf.go` — double-submit CSRF for cookie-authenticated forms
- `middleware.go` — `ProxydTransit` (proxyd bearer transit proof)
- `web.go` — `SafeReturn`/`JSSafe`, the return-URL and script-escape helpers
- `oauth.go` — provider exchange/userinfo + state/PKCE wrappers (authd owns the routes)
- `surrogate/` — outbound `Connect <provider>` OAuth (spec 5/15): arizuko acts AS the user against a third-party API, writing the token into the user-scoped `secrets` row

## Related docs

- `ARCHITECTURE.md` (Auth Hardening)
- `specs/5/32-acl-unified.md` — canonical ACL
- `specs/5/17-openapi-mcp.md` — full token contract; auth/ is the
  single source of truth for the JWT shape every federated daemon
  consumes
