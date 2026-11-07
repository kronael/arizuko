# auth

Identity, ES256 tokens, JWKS, OAuth, authorization policy, HTTP middleware.

## Authorization is one row and one question

Every check asks: may principal P do action A on scope S, with these
arguments? One row type describes local permissions and the defaults in
`auth/defaults.go`. `Policy.Allows` decides each request. The REST and MCP
faces supply different callers and target bounds.

<!-- twin:grants -->

```
┌─────────────────────────────────────────────────────────────────────────┐
│ caller: REST uses the JWT sub; MCP uses the sender and turn folder      │
│ acl_membership expands each principal through accounts and roles        │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Consistent policy read: memberships and local acl rows.                 │
│ auth/defaults.go supplies the default layer.                            │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ Authorize → Policy.Allows: matching local rows decide first.            │
│ Defaults decide only when no local row matches.                         │
│ Deny wins in that layer. No matching allow means refusal.               │
│                                                                         │
│ EffectiveActions lists allowed tool actions from both layers.           │
│ CheckGrant checks the rule mutation and returns its policy version.     │
└─────────────────────────────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────────────────────────────┐
│ MCP route axis: the turn folder and its roles confer in full.           │
│ Identity axis: sender allows require a target inside the turn subtree.  │
│ Identity allows match the exact action or its namespace wildcard.       │
│ Deny rows count on both axes, within the selected policy layer.         │
└─────────────────────────────────────────────────────────────────────────┘
```

<!-- /twin -->

- **Matching local rows decide first.** `ReadPolicy` reads membership and
  local rows consistently, then adds the defaults from `auth/defaults.go`.
  `Policy.Allows` uses defaults only when no local row matches the call.
  Deny wins within the selected layer. No matching allow means refusal.
  A failed policy read refuses the request. Folder depth grants nothing.
- **Containment is the scope glob.** Guest capabilities (`share_mount`,
  `egress`, `web:publish`, `gh:write`, `codex`) resolve from `acl` at dispatch into
  typed booleans on the run request. A capability derived from folder depth
  reaches every folder at that depth, whatever the operator granted, so no
  code derives one. `ARIZUKO_TIER` is not emitted: the agent's identity is
  its world, path and grants, never a rank.
- **No cache.** Each ordinary non-elevated tool gate reads the current policy.
  A revoke therefore takes effect on the next checked call inside a live turn.
  The turn socket supplies the credential without a token.
- **Membership is one graph.** `acl_membership` carries roles, role
  hierarchy and the pairing edges that let a channel JID act as an account.
  The caller expands to its sender JID and room JID, transitively. A write
  that would make a principal its own ancestor is refused
  (`store.MembershipWouldCycle`), and a manifest apply runs the same walk.
- **The agent socket has two axes** (`AuthorizeTurn`). The route,
  `folder:<turn folder>`, confers in full. The sender adds an allow only
  inside the turn folder's subtree, on the exact action or its namespace
  `*`, with no lattice; a glob target is inside no subtree. A deny counts on
  both axes within the selected layer. So outside the turn's subtree the verdict does not depend on
  who spoke, and a shared chat is not a place to borrow authority.
  `EffectiveActions` applies the same rule to `tools/list`.
- **One principal pair per turn.** `routd.turnAuthorize` closes over the
  sender and the folder, so no call site can supply a third principal.
  Stacking two notions of "who" is how a confused deputy gets built.
- **Three reads stay single-axis:** the dispatch capabilities,
  `ShellEnvKeys` and `CheckHold`. The sandbox must not be a function of the
  conversation.
- **Hold and shell actions use isolated namespaces.** `CheckHold` and
  `ShellEnvKeys` call `Policy.Allows`. `actionCovers` accepts the exact action
  or its namespace wildcard. Other action grants do not cover these namespaces.
- **Visibility is not the gate.** `EffectiveActions` lists a tool when the
  caller holds its action at any scope; a lattice row (`*`, `admin`,
  `mcp:*`) lists every `mcp:` tool. A deny row is scope-specific, so it hides
  nothing: `Authorize` refuses the call instead.
- **A bearer's scope is not a grant.** `HasScope` checks what a token
  carries; `Authorize` checks what a principal holds. The two are never
  merged, because a leaked token must carry less than its owner.
- **Elevation is explicit.** `IsOperator` checks admin on `**` before `/root`.
  An elevated turn bypasses ordinary tool gates for one turn. Rule mutations
  still pass `CheckGrant` with the issuer's authority.
- **Rule mutations use `CheckGrant`.** Added allows and removals outside
  `hold:` require covering authority with `grant_option`. Hold mutations
  require admin authority without the grant option. Adding a non-hold deny
  narrows access. A covering allow must have no predicate. Its scope must cover
  the requested scope. Its params must be empty or identical to the requested
  params. For containment, a local row with an overlapping action and scope
  selects the local layer. Otherwise defaults decide. A deny in the selected
  layer refuses the authority check. Identity-only allows cannot supply covering authority.
  An added allow cannot name a wildcard principal.
  Edge additions check the parent's allows and hold rules in both layers.
  Edge removals check the parent's denies, hold rules and local allows.
  The check returns a policy version for the write to prove.
  Host CLI and manifest writes, consent pairing and folder bootstrap use
  their own authority.
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
2. **One authorization evaluator.** `Authorize` reads a policy over the
   caller's expanded principal set and calls `Policy.Allows`. REST and MCP
   supply the identity and target bound.
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
- Authorization: `ReadPolicy` reads the local and default layers consistently.
  `Policy.Allows` evaluates a request; `Authorize` reads and evaluates it.
  `EffectiveActions` lists tool actions with an allow at any scope in either
  layer. The per-call gate still enforces denies and target bounds.
  `UserScopes` lists allow scopes from both layers. It includes `**` only
  when the policy allows admin on `**`. A scope list is not a verdict.
  `CheckGrant` checks rule mutations and returns their policy version.
  `ShellEnvKeys` answers which `shell:env:<KEY>` secrets a
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
  `ExchangeDiscord`/`FetchDiscordUser`,
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

## Files

- `identity.go` — `Identity` and `Resolve`: a folder's own name plus the root predicate, and nothing else
- `acl.go` — `MatchGroups`, scope-glob matching
- `authorize.go` — `Authorize`, `EffectiveActions` (ACL row-grant gate, `Caller`)
- `defaults.go` — the default policy
- `grant.go` — `CheckGrant`, rule containment and policy versions
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
