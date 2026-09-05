---
status: partial
depends: [16-daemon-standalone-matrix, ../5/7-proxyd-standalone]
---

# specs/6/22 — cut proxyd to its irreplaceable half

`6/16` already states the position: proxyd is "the commodity half —
Caddy/Traefik plus Authelia's job, and the weakest position in the repo",
and the move is "shrink toward off-the-shelf and publish the
grant-scoped mount contract, not extract a bundle."

**This spec does not remove proxyd.** It states what proxyd is actually
made of, and orders the cuts so each one stands alone. Step 1 is worth
doing whether or not an off-the-shelf proxy is ever adopted.

## What is in there

1,631 lines of non-test Go, and **3,955 lines of test** — more than two
lines of test per line of code, most of it exercising hand-rolled
proxying. That ratio is the strongest argument in this document: it is
the cost of owning a commodity.

Four jobs live in one daemon:

| Job                               | Verdict                                                                                                                                                                                                                                                     |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| route matching + reverse proxying | commodity. Caddy and Traefik do it, better tested                                                                                                                                                                                                           |
| TLS termination                   | commodity, and Caddy does automatic certs                                                                                                                                                                                                                   |
| folder + `acl` scoping            | **irreplaceable** — `auth.MatchSlot`, per-folder mounts, no parent inheritance (`5/10`). No off-the-shelf proxy has a folder-agent                                                                                                                          |
| bespoke token handlers            | **misfiled** — `/chat/<token>` (`dispatchRouteToken`) and `/hook/<token>` were application surface, not proxying. Moved to webd, step 1. The vhost redirect (`worldForHost`/`vhostRedirect`) was listed here too and does not belong: see "Not moved" below |

## Step 1 — move the misfiled handlers out (DONE for /chat/ + /hook/, 2026-08-18)

`/chat/<token>` and `/hook/<token>` terminated in proxyd: it resolved a
route token, minted identity and dispatched. That is `webd`'s job — the same
token-chat, SSE and `/hook` surface `6/16` already calls "the agent
surface, not a web-stack part".

None of this is proxying, and none of it depends on the proxy decision.
Moving it makes proxyd honest **either way**: if the off-the-shelf swap
never happens, proxyd is still a proxy rather than a proxy with an
application stapled to it.

### What shipped

`dispatchRouteToken` is gone, and with it the `switch rt.Path` arm naming
`/chat/` and `/hook/`. proxyd no longer knows either path.

- **Token resolution deleted, not moved.** webd already resolved the token in
  every handler (`lookupRouteToken`); proxyd resolved the same row a second
  time to stamp `X-Chat-Token` / `X-Folder` / `X-Group-Name`. One row, one
  reader now. `X-Group-Name` had no reader at all.
- **The anon DoS shield moved** to `webd/anonshield.go`, keyed on the
  `X-Forwarded-For` client proxyd resolves — not `RemoteAddr`, which behind
  proxyd is one address for the whole world (`L40`).
- **`auth: optional` replaces the path special-case.** proxyd still does the
  one thing only proxyd can: identify a caller who offers a bearer, and prove
  transit. It is now a route attribute, so the rows are ordinary rows.

### The bug this exposed

proxyd attached its `service:proxyd` transit bearer only inside
`setUserHeaders`, i.e. only for callers it authenticated. An anonymous
`/chat/` request therefore reached webd with no proof it had transited proxyd
— and `/chat/stream`'s gate demanded exactly that proof. **On every instance
with a JWKS configured, the chat widget's live stream answered 403 to every
anonymous visitor.** It could only pass in local dev, where a nil KeySet
short-circuits the check.

`optional` attaches the proof on both paths, which fixes it. The gate itself
survives, restated as what it always meant: `transited(r)`, the bearer, with
none of the token-stamp indirection.

One property did NOT survive: the gate used to distinguish "proxyd resolved a
token for this request" from "proxyd merely authenticated someone", so a
signed-in user whose grants missed the token's folder was refused. proxyd no
longer resolves tokens, so that distinction is unreachable by relocation. It
was nominal — the refused caller had to be holding the route token, which
already lets them POST into the chat and read its history through sibling
endpoints that have never had a gate. The test now asserts the containment
that does the work: `?group=` cannot aim the subscription (`L30`).

### Not moved: the vhost redirect

`worldForHost` / `vhostRedirect` stay in proxyd. They are not addressable by a
`proxyd_routes` row: both call sites are proxyd's _fallback_, one at bare `/`
(entangled with websocket upgrade to vited and external `PUB_REDIRECT_URL`
probing) and one at the unmatched-path catch-all. Routing either to webd means
changing what proxyd does with **every** unmatched request fleet-wide, and
makes the world front door depend on webd being up.

The redirect is also not the application surface the table above calls it: it
emits `/pub/<world>/<path>`, the same shape as the plain `/pub` fallback
beside it, into a path vited serves and webd does not. It belongs to whoever
owns the `/pub` fallback. Either amend the table's verdict, or spec the
catch-all change on its own — it is a redesign, not a relocation.

## Step 2 — routes become rows (unblocked 2026-08-17, signed off, renderer SHIPPED 2026-09-04)

Config for any off-the-shelf proxy must be **rendered** from somewhere.
It cannot be rendered from `PROXYD_ROUTES_JSON`, which was seed-once and
ignored after first boot — the fault `L45` records, and the reason krons
was missing 7 of the 18 routes arizuko ships. **`L45` was fixed 2026-08-17**:
`arizuko generate` writes the rows (`store.SyncProxydRoutes`) and proxyd's
boot path reads rows only. The prerequisite is therefore done and the
remaining work is the renderer itself, which is unbuilt.

With routes as rows, the proxy config becomes one more reader of one
declaration (root `CLAUDE.md`, "one declaration, many readers").

**Shipped 2026-09-04: `proxycfg` + `arizuko proxy-config <instance>`.** It
renders a Caddyfile rather than Caddy's admin JSON — `forward_auth` is a
first-class directive there, so the one thing the render cannot do stays legible
instead of hiding in a `handle_response` block. Measured against krons' live
table: 25 rows in, 25 routes out.

Three things the render has to get right, each pinned by a mutation-checked
test. **Order**: proxyd matches longest-prefix, Caddy matches `handle` blocks in
written order, so emission order carries the semantics. **`optional` takes the
auth hop**: that is where proxyd identifies a caller who offers a credential, so
skipping it drops attribution on the route-token surface. **`gated_by` is NOT
re-evaluated**: it is generate-time-only metadata (`proxyd/routes.go:21`) and
compose drops a gated route before writing the row, so every row that exists has
already passed. Re-gating on the render dropped two LIVE krons routes (ttsd,
davd) whose vars live in the generated `env/` rather than `.env` — the `L45`
shape a second time, a second evaluation of one decision.

**It prints; it does not install.** proxyd is still the running proxy, and an
authenticated route renders as `respond 503` with the reason until step 3's
forward-auth endpoint exists — rendering the proxy hop without it would serve
the route to anyone. That refusal is the honest boundary between the two steps.

## Step 3 — the auth split (endpoint SHIPPED 2026-09-04; cutting over to an off-the-shelf proxy is still unsigned)

What stays arizuko's becomes a **forward-auth endpoint**: the proxy makes
a subrequest per request; arizuko answers 200 plus `X-User-*`, or 401.
This is Traefik `ForwardAuth` / Caddy `forward_auth` / nginx
`auth_request`.

Two constraints that decide whether this is safe:

- **The transit proof rides the auth response, never the proxy's config.**
  `auth.ProxydTransit` proves the request came through proxyd, and backends
  verify that bearer before trusting `X-User-*`. A long-lived token in the
  proxy's config is a downgrade: a file on the proxy host is then enough to
  impersonate proxyd for as long as the file exists. Copying proxyd's own
  bearer into each response bounds that exposure to the bearer's TTL — 15
  minutes (`authd/main.go:25`), the same bearer proxyd attaches to every
  proxied request today.
- **Authorization is never cached.** One subrequest per request, always —
  a cached authz decision is a revocation that does not take effect. On
  localhost the cost is a fraction of a millisecond; it is still a real
  per-request tax where today the check is in-process.

### Shipped 2026-09-04 — `GET /auth/forward` on proxyd

The endpoint exists; the cutover does not. proxyd answers a proxy's auth
subrequest with 200 + `Authorization` (its own `service:proxyd` bearer),
`X-User-Sub`, `X-User-Name`, `X-User-Groups` — or the same 303-to-login a direct
request gets, with `auth_return` set to the CALLER's route read from
`X-Forwarded-Uri`, not to `/auth/forward`.

Both constraints above are honoured and pinned, in the form the code has. The
proof is copied into each response from `auth.TokenSource` (`attachTransit`,
`proxyd/main.go:773`), which re-exchanges `AUTHD_SERVICE_KEY` one minute before
the 15-minute expiry. It is **not minted per request**: every response inside
one window carries the same bearer, and a proxy host that reads one response
can present it to any backend for the rest of that window. A per-request mint
is one authd round trip per request — the tax this step already accepts for
authorization — and `L191` records that choice as open. The decision is
**not cached** — one subrequest, one answer.

`decideForwardAuth` deliberately runs `requireAuth`'s attempts in
`requireAuth`'s order rather than deriving its own: a second authentication path
beside the one proxyd already applies is the drift this spec exists to remove.
One divergence is recorded rather than hidden — the refresh-via-authd attempt
needs a `ResponseWriter` to re-set the cookie, and a subrequest has no upstream
response to attach it to, so an expired access token takes the login flow
instead. One extra round trip, no silent difference in who is admitted.

**A test that passed against broken code, and what fixed it.** The first version
asserted only that the answer did not echo the caller's bearer. With `svc` nil
in the fixture, `attachTransit` DELETES `Authorization` and sets nothing, so the
assertion passed on an endpoint that attached no proof at all — and the mutation
that removes `Authorization` from the copied set stayed green. The fixture now
stands up a stub authd so the proof is real and the test asserts its PRESENCE —
presence, not freshness: a test asserting a distinct `jti` per response fails
today (`L191`).

## Step 4 — could a standard IdP let us DELETE authd? (measured 2026-09-05: no)

Step 3 asks the question for proxyd. The same question for `authd` has a
sharp answer. The auth surface does not split into a commodity half and a
product half. It splits into an upstream provider dance, which Dex takes, and
everything else, which no IdP can hold because it mints arizuko's claims.

### The measurement

Six files carry authn: `auth/oauth.go`, `authd/oauth.go`, `auth/es256.go`,
`auth/jwks.go`, `authd/sessions_resource.go`,
`authd/signing_keys_resource.go` — 1639 lines by `wc -l` at every commit since
2026-09-02, 1260 with blanks and comments removed (the figure 1618 reproduces
by neither count). Counting the whole of them as commodity is the error; the
classification is what decides the step. Read function by function:

| what the six files hold                                                                                                                                               | lines   | who else does this                                                                                                                               |
| --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| upstream provider dance — Google/GitHub/Discord code exchange + userinfo fetch, GitHub org check (`auth/oauth.go` `Exchange*`/`Fetch*`, `authd/oauth.go` `*Callback`) | **345** | Dex connectors: `github` (`orgs`, `useLoginAsID`), `google`, generic `oauth`                                                                     |
| relying-party glue — signed CSRF state, PKCE cookies, `auth_return`, login page, callback prologue                                                                    | 280     | nobody: authd is the relying party to WHATEVER issues, and `authd/dex.go` calls every one of these                                               |
| identity + session glue — alias resolution (`dispatch`), login-time scope snapshot, `arz/folder` mint, refresh family, audit row, `/auth/me`, logout                  | 155     | nobody                                                                                                                                           |
| Telegram login widget, Google email-glob allowlist                                                                                                                    | 77      | nobody — the widget is an HMAC form POST, not OAuth, and no Dex connector has that shape; Dex's google connector filters on `hostedDomains` only |
| mint, verify, rotate, list sessions, key metadata (`auth/es256.go`, `auth/jwks.go`, `authd/sessions_resource.go`, `authd/signing_keys_resource.go`)                   | 728     | every IdP — for ITS claims. None can mint `arz/folder` or the grants snapshot, so it stays while arizuko's tokens carry them (below)             |
| imports and file headers                                                                                                                                              | 54      | —                                                                                                                                                |

**345 of 1639 lines are the commodity.** `authd/dex.go`, the relying-party
side of the broker, is 126 lines, so adopting Dex nets a cut of about 220
lines against a new daemon.

### Why the 728 lines of minting stay

Dex's ID token carries a fixed claim set — `email`, `groups`, `name` /
`preferred_username`, `federated_claims` — a closed `switch` over four scopes
in `server/tokens/issuer.go` with no per-user claim hook
([source](https://github.com/dexidp/dex/blob/master/server/tokens/issuer.go)).
It cannot carry `arz/folder`, which eight files read (`routd/cmd/routd/main.go`,
`runed/cmd/runed/main.go`, `onbod/audit_resource.go`, `cmd/arizuko/token.go`,
…), nor the login-time scope snapshot that bounds every token (`5/1`). Either
arizuko mints — and then `es256.go`, `jwks.go`, the refresh families and the
key metadata all stay, because they ARE the mint — or every backend looks the
folder and the grants up in routd.db per request, which is the pre-split
topology `5/16` removed and the database-at-the-verifier `6/25` §2 refuses.
Authelia's `claims_policies` (4.39+) map static attributes from its file or
LDAP store, not a live grants table; Keycloak's protocol mappers can compute a
claim and bring a JVM plus an RDBMS (`6/16`). Nothing short of a fork puts
arizuko's claim into an IdP's token.

Downscoping is arizuko's too: `POST /v1/tokens` in downscope mode
(`authd/http.go:311`, `parent_jti`, TTL capped at the parent's) has no Dex
analogue — Dex narrows none of its own tokens, on either side.

### The service token does not become a client-credentials grant

Dex v2.45.1, the latest release (2026-03-03), has no `client_credentials`
grant: `server/oauth2.go` at that tag lists `authorization_code`,
`refresh_token`, `implicit`, `password`, `device_code` and `token-exchange`
([source](https://github.com/dexidp/dex/blob/v2.45.1/server/oauth2.go)). The
grant exists on master, unreleased, and refuses the `federated:id` scope
([source](https://github.com/dexidp/dex/blob/master/server/grants/clientcredentials.go)).
It would not help if it shipped: `auth.ProxydTransit` pins `typ=service` and
`sub=service:proxyd` on arizuko's own verifier, and `serviceGrants`
(`authd/http.go`) is arizuko's scope table. `POST /v1/service-token` and
`auth/service.go` stay, and neither was in the count.

### What adopting Dex costs

All of it is paid before the 220 lines are cut.

- **A second daemon with a second store.** Dex's own docs call its SQLite
  backend "not appropriate for real workloads"
  ([storage](https://dexidp.io/docs/configuration/storage/)): production means
  Postgres, MySQL or etcd beside arizuko's SQLite files, or running against
  that advice. Its SQLite build is CGO `mattn/go-sqlite3`, not the pure-Go
  driver the rest of the stack uses.
- **A second secrets file.** Every upstream client id and secret moves from
  `.env` into Dex's YAML, plus a Dex client for authd (`DEX_CLIENT_ID`,
  `DEX_CLIENT_SECRET`). Two places to rotate.
- **Alpha connectors on the login path.** Dex's own table marks `github`
  "stable", `google` "alpha" and the generic `oauth` connector "alpha"
  ([connectors](https://dexidp.io/docs/connectors/)). Discord and Facebook
  ride the alpha one, X does not work through it (the spike below), Telegram
  has no connector shape at all.
- **Two hops per login, one more daemon that can be down.** Browser → authd →
  Dex → upstream → Dex → authd. With the native callbacks deleted, Dex down
  is login down; with them kept, nothing is deleted.
- **Dex's admin surface is gRPC only** (`api/v2/api.proto`: `ListRefresh`,
  `RevokeRefresh`, `ListAuthSessions`, …). It is broader than `/v1/sessions`
  and unreachable from `curl` or the dashboard without a gRPC client.
- **A migration with no rows to move, and one trap.** Principals stay
  `<connector id>:<user_id>`, so `acl` rows survive — only if the github
  connector sets `useLoginAsID: true`. Without it every `github:<login>` row
  matches nothing and the config looks applied (`L45`'s shape).
- **Key rotation moves to `signer.config.keysRotationPeriod`** and loses the
  lifecycle view: Dex publishes `/keys` and nothing about a key's life, the
  gap `GET /v1/signing_keys` closed.

### Which IdP (surveyed 2026-09-03)

Requirements, in the order that decided it: federates to the upstream providers
arizuko already uses (`github`, `google`, `discord` in `authd/oauth.go`, plus
Twitter/X and Facebook wanted), Go, minimal, maintained. Each row was read from
the project's own repo or docs, not from memory.

| candidate    | Go / licence                         | what it IS                                                                                          | verdict                                                           |
| ------------ | ------------------------------------ | --------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| **Dex**      | Go, Apache-2.0, 11.1k★, 4039 commits | "OIDC identity and OAuth 2.0 provider with pluggable connectors" — a BROKER; holds no users         | **selected**                                                      |
| Ory Hydra    | Go, Apache-2.0                       | "standalone OAuth 2.0 and OIDC server **without user management**"; you build the login+consent app | rejected — deletes less                                           |
| Authelia     | Go, Apache-2.0                       | forward-auth SSO portal with its OWN user store (file/LDAP); OIDC-provider role is beta             | rejected as the IdP — it is step 3's gateway, not step 4's broker |
| Casdoor      | Go+React, Apache-2.0, 14.3k★         | full IAM: admin console, own user directory, **Casbin ACL/RBAC/ABAC**                               | rejected — brings a second authz model                            |
| oauth2-proxy | Go, MIT                              | a relying-party reverse proxy: authenticates against an IdP, holds a proxy session, mints nothing   | not a candidate — it is step 3's shape, not an issuer             |

**Dex, because it is the only one that is a broker rather than a directory.** It
holds no users and ships no authorization model, so it takes the provider dance
and touches none of the 1044 authz lines (`auth/authorize.go`, `acl.go`,
`delegate.go`, `scope.go` — the `[!]action(param=glob)` lattice, `WITH GRANT
OPTION` delegation and `5/33`'s two-axis rule, which no standard expresses).
Hydra hands the upstream federation back as our problem, so it deletes
the smaller half of the smaller half. Casdoor would install Casbin beside a
grant evaluator we are keeping; two authorization engines is worse than one
hand-rolled.

Native connectors cover github and google. **discord, Twitter/X and Facebook
have no named Dex connector** and would ride the generic `oauth` connector —
configurable `authorizationURL` / `tokenURL` / `userInfoURL` / `scopes` /
`claimMapping`, which is the right shape, but the docs state no stability marker
and X's OAuth2 wants PKCE with a non-standard userinfo. **One spike proves or
kills this before anything is deleted**: stand Dex up with the generic connector
against Discord and X, and confirm the claim mapping yields the same
`discord:user/<id>` principal `acl_membership` already holds. If it does not,
the authn half stays hand-rolled and this step dies there.

### The spike (run 2026-09-03, Dex v2.45.1)

Dex on the host network, three generic `oauth` connectors pointed at a mock
that answers each provider's documented userinfo shape, the code flow driven
with curl, scope `openid profile federated:id`. What the ID token carried:

| connector  | userinfo shape               | ID token                                                                       | verdict                          |
| ---------- | ---------------------------- | ------------------------------------------------------------------------------ | -------------------------------- |
| `discord`  | `{"id":"8112…","username"…}` | `"federated_claims":{"connector_id":"discord","user_id":"811295670702047272"}` | **green**                        |
| `facebook` | `{"id":"1022…","name"…}`     | `"federated_claims":{"connector_id":"facebook","user_id":"1022…"}`             | green                            |
| `twitter`  | `{"data":{"id":…}}`          | none — Dex: `OAuth Connector: not found id claim`                              | **red**, and red before that too |

The Discord row is the kill condition, and it passed: `user_id` is the raw
Discord snowflake, so authd can render it as either `discord:<id>` (what the
native callback mints today) or `discord:user/<id>` (the JID form
`acl_membership` holds on krons). **Those are two principals today**, bridged
only by a `5/31` pairing edge; the spike does not collapse them — that is a
`5/32` namespace decision, not an IdP one.

X fails the generic connector twice, both verified against the source and
X's docs: the connector builds its login URL with `oauth2.Config.AuthCodeURL(state)`
and no PKCE, while X's authorization-code grant is PKCE-only; and it reads
`userInfoResult[userIDKey]` at the top level, while `GET /2/users/me` nests the
id under `data`. X therefore needs either a Dex connector contributed upstream
or arizuko's own callback — the generic connector cannot carry it.

Two Dex facts the mapping rests on: `federated_claims` is emitted only for the
`federated:id` scope (`server/oauth2.go`), and the github connector's `user_id`
is the numeric GitHub id unless its config sets `useLoginAsID: true` — the
krons row `github:kronael` keys on the login, so a Dex github connector must set
it or the operator's grant stops matching.

### What shipped (phase B, additive)

`authd` accepts a Dex login beside the native ones: `DEX_ISSUER` +
`DEX_CLIENT_ID` (+ `DEX_CLIENT_SECRET`) mount `GET /auth/dex` and
`/auth/dex/callback`, unset leaves the daemon byte-identical. `authd/dex.go`:
discovery on first use (authd boots with Dex down), the redirect through the
same signed-state + PKCE cookies as every provider, the code exchange, go-oidc
verification of issuer, audience, expiry and signature, then
`federated_claims` → `dispatch(connector_id, user_id)`. Everything after that
point — alias resolution, the `auth_users` row, the scope snapshot, the
`arz/folder` claim, the ES256 mint, refresh — is the existing path. The
connector `id` in Dex's config IS the principal namespace; authd keeps no
allowlist, so a new connector is a new namespace with no arizuko code.

Not touched: `auth.ProxydTransit`, `/v1/service-token`, the native callbacks,
and all of `auth/authorize.go`, `acl.go`, `delegate.go`, `scope.go`. Nothing
is deleted; the delete waits on a deployment running Dex for its logins.

**Decision (2026-09-05): Dex is an optional upstream broker, not a
replacement.** Phase B stays as shipped — additive, off by default, one more
`/auth/<provider>` beside the native ones, for an operator who already runs
Dex. That is where a broker earns its place: an operator who has one. Nothing
is deleted. As a trade the delete loses on its own terms: the three native
callbacks are 345 lines with no dependency, and their replacement is a daemon,
a store its docs say should not be SQLite for real workloads, and alpha
connectors for two of the three providers. The other 1294 lines stay in every case, because they mint
the claim the platform runs on.

## What this spec does NOT propose

Removing proxyd, choosing between Caddy and Traefik, or moving TLS. Those
follow step 3's cutover and are decided then, on evidence from the steps
before them. Step 4 is decided: no IdP replaces authd.

## Ties

`6/16` (the position, and the four-layer map) · `5/7` (proxyd standalone —
already specs it droppable) · `5/10` (per-folder mounts, no parent
inheritance — the irreplaceable mechanism) · `5/W` (the route-token surface
step 1 consolidates in webd) · BUGS `L45` (routes as rows, step 2's
prerequisite) · `L44` (packages declare through the manifest) · `L40` (the
shield keys on the client, not the hop — the property that had to survive the
move) · `L4` (folder from the row, not the JID grammar — one resolver now) · `L191`
(the forward-auth proof is the cached service bearer, not a per-request mint).
