# auth

Identity, ES256 tokens, JWKS, OAuth, authorization policy, HTTP middleware.

## Authorization is one row and one question

Every check asks: may principal P do action A on scope S, with these
arguments? One `acl` row type answers it, and three readers read the same
rows. The operator's REST face and the agent's MCP socket differ only in
where the caller comes from.

<!-- twin:grants -->

```
┌─────────────────────────────────┐   ┌───────────────────────────────────┐
│ caller                          │   │ acl row                           │
│ operator REST: the JWT sub      │   │ principal, action, scope,         │
│ agent socket: the turn's sender │   │ params, predicate, effect,        │
│ and folder:<turn folder>        │   │ grant_option                      │
└────────────────┬────────────────┘   └─────────────────┬─────────────────┘
                 │ expanded through acl_membership,     │
                 │ transitive: role:member,             │
                 │ role:operator, roles, the account a  │
                 │ paired JID acts as                   │
                 ▼                                      ▼
┌───────────────────────────────────────────────────────────────────────────┐
│ the rows that reach the expanded principal set, read on every call        │
└──────┬─────────────────────────┬─────────────────────────────┬────────────┘
       ▼                         ▼                             ▼
┌──────────────────────┐ ┌──────────────────────┐ ┌───────────────────────┐
│ Authorize            │ │ EffectiveActions     │ │ Delegate              │
│ the per-call gate:   │ │ tools/list: the      │ │ add_acl: each new row │
│ scope glob, params,  │ │ action held at any   │ │ a subset of the rows  │
│ predicate; deny      │ │ scope; a deny row    │ │ held WITH GRANT       │
│ wins, no row = deny  │ │ hides no tool        │ │ OPTION; never a deny, │
└──────────────────────┘ └──────────────────────┘ │ never a wildcard      │
                                                  │ principal             │
                                                  └───────────────────────┘
┌───────────────────────────────────────────────────────────────────────────┐
│ AuthorizeTurn, the agent socket's two axes. Route: folder:<turn folder>   │
│ and its roles confer in full. Identity: the sender and its roles add an   │
│ allow only inside the turn folder's subtree, on the exact action, with no │
│ lattice. A deny counts on both axes, everywhere.                          │
└───────────────────────────────────────────────────────────────────────────┘
```

<!-- /twin -->

- **No row is a deny.** Deny wins; an action with no matching allow row is
  refused, loudly. A path carries zero authorization: it is a routing
  target, a JID prefix, a home and a vhost, and its depth means nothing.
- **Containment is the scope glob.** Guest capabilities (`share_mount`,
  `egress`, `web:publish`, `gh:write`) resolve from `acl` at dispatch into
  typed booleans on the run request. A capability derived from folder depth
  reaches every folder at that depth, whatever the operator granted, so no
  code derives one. `ARIZUKO_TIER` is not emitted: the agent's identity is
  its world, path and grants, never a rank.
- **No cache.** Every call reads the rows, so a revoke takes effect on the
  next tool call inside a live turn. The turn holds no token at all: the
  socket is its credential, so no baked scope outlives a revoked grant.
- **Membership is one graph.** `acl_membership` carries roles, role
  hierarchy and the pairing edges that let a channel JID act as an account.
  The caller expands to its sender JID and room JID, transitively. A write
  that would make a principal its own ancestor is refused
  (`store.MembershipWouldCycle`), and a manifest apply runs the same walk.
- **The agent socket has two axes** (`AuthorizeTurn`). The route,
  `folder:<turn folder>`, confers in full. The sender adds an allow only
  inside the turn folder's subtree, on the exact action or its namespace
  `*`, with no lattice; a glob target is inside no subtree. A deny counts on
  both axes. So outside the turn's subtree the verdict does not depend on
  who spoke, and a shared chat is not a place to borrow authority.
  `EffectiveActions` applies the same rule to `tools/list`.
- **One principal pair per turn.** `routd.turnAuthorize` closes over the
  sender and the folder, so no call site can supply a third principal.
  Stacking two notions of "who" is how a confused deputy gets built.
- **Three reads stay single-axis:** the dispatch capabilities,
  `ShellEnvKeys` and `CheckHold`. The sandbox must not be a function of the
  conversation.
- **Two readers match the action exactly.** `CheckHold` and `ShellEnvKeys`
  skip the action lattice, because through it the operator's `(*, **)` row
  would hold every tool and hand every folder secret to every operator
  turn.
- **Visibility is not the gate.** `EffectiveActions` lists a tool when the
  caller holds its action at any scope; a lattice row (`*`, `admin`,
  `mcp:*`) lists every `mcp:` tool. A deny row is scope-specific, so it hides
  nothing: `Authorize` refuses the call instead.
- **A bearer's scope is not a grant.** `HasScope` checks what a token
  carries; `Authorize` checks what a principal holds. The two are never
  merged, because a leaked token must carry less than its owner.
- **Elevation is explicit.** `/root` is the one path outside the subtree:
  gated on `IsOperator`, one-shot, and an elevated turn passes every gate.
- **The magnitude gate passes no arguments** for tools registered through
  `ipc` `granted()`, so a row with `params` matches nothing there: a deny on
  such a tool is tool-wide only. The messaging tools pass `jid`, so a `jid=`
  row works. `CheckHold` receives the arguments, so a hold can be shaped.
- **A predicate on a `folder:` principal never matches.** The agent socket
  carries no JWT claims. "Act as guest" is one row whose predicate names the
  guest, on a user principal; `packages install` refuses the folder form.

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
   root predicate.
4. **The OAuth exchange half.** Pure exchange/userinfo wrappers plus state,
   PKCE and cookie helpers; authd owns every route.

## The platform token

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
Scopes are `<resource>:<verb>` pairs, optionally `<resource>:<verb>:own_group`,
the same verb confined to the caller's own folder subtree. Wildcards are
namespace-only (`tasks:*`), and there is no global `*:*`: operators carry
the enumerated resource list.

**Signing** (`SigningKey`, ES256 P-256 keypair; `Kid` rides the JWS
header for rotation):

- `(*SigningKey).Sign(c TokenClaims, ttl) (string, error)` — low-level
  mint; forces `iss=authd`, sets `iat/nbf/exp`, fills `jti`.
- `(*SigningKey).MintForSubject(targetGrants []string, c, ttl)` —
  authoritative issuance after OAuth/service-key exchange; granted scope
  = requested ∩ targetGrants (all of targetGrants if requested empty).

The crypto stack is fixed. go-oidc's `RemoteKeySet` fetches and caches
authd's JWKS. arizuko checks its own claims rather than run
`IDTokenVerifier`, because it mints its own tokens; `IDTokenVerifier` checks
only an external provider's ID token (Dex). go-jose signs ES256. arizuko is
not an OAuth or OIDC server, so it links no server framework and no
hand-rolled JWT code.

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
  for human approval. `ValidateParams` / `ParamsSpec` are
  the param-glob half.
- Token verify: `VerifyToken(token, ks)`, `VerifyHTTP(r, ks)`,
  `HasScope(scope, resource, verb)`, `HasScopeCoveredBy`.
- JWKS: `PublicJWKS(keys…)` (authd's `/v1/keys`); `FetchKeys(ctx, authdURL)`,
  `NewKeySet`, `NewKeySetWithRetirement` (backend-side cache).
- Service bootstrap: `ServiceToken(authdURL, daemon, key) (*TokenSource, error)`;
  `(*TokenSource).Token(ctx)` returns a live, auto-refreshed `service:<name>`
  token.
- Dashboard gate: `RequireOperator`, `RequireWrite`, `Forbidden` — the ONE
  operator gate every per-daemon dashboard applies. One gate, because three
  copies drift into answering differently per daemon.
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
- `es256.go` — `TokenClaims`, `Subject`, `SigningKey`, `Sign`/`MintForSubject`
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
- `surrogate/` — outbound `Connect <provider>` OAuth — arizuko acts AS the user against a third-party API, writing the token into the user-scoped `secrets` row

## Related docs

- [`authd/README.md`](../authd/README.md) — the signer: keys, mint modes,
  refresh lineage, login.
- [`SECURITY.md`](../SECURITY.md) — where this evaluator sits in the trust
  model.
- [`reference/grants.html`](../template/web/pub/arizuko/reference/grants.html)
  — the operator's row reference: lattice, holds, delegation, examples.
