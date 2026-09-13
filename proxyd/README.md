# proxyd

Reverse proxy: public `/pub/`, auth-gated everything else.

## Purpose

Single entry point for web traffic. Handles authentication (JWT +
refresh-token cookie) and routes by path prefix to backends. Stamps forwarded
identity headers with a service bearer (`service:proxyd`) verifiable by
backends.

proxyd does not serve the route-token surface. `/chat/<token>` and
`/hook/<token>` are ordinary rows pointing at webd, which resolves the token
and owns the anon DoS shield (`specs/6/22-proxyd-cut.md` step 1).

## Responsibilities

- Authenticate: `Authorization: Bearer <jwt>` (ES256, against authd's JWKs) → `refresh_token` cookie → redirect `/auth/login`.
- Stamp `X-User-Sub`, `X-User-Name`, `X-User-Groups` + `Authorization: Bearer service:proxyd`.
- Route by DB-backed prefix table (runtime mutate via `/v1/proxyd_routes`).
- Rewrite `X-Forwarded-For` from `TRUSTED_PROXIES` CIDRs only.
- Derive each world's host (`<world>.<HOSTING_DOMAIN>`) and 302 it to
  `/pub/<world>/`; `WEB_VHOST_ALIASES` overrides labels ≠ world name
  (`specs/5/V-web-vhosts.md`).
- Honor agent-registered web_routes (redirect/deny/auth gating on `/pub/*`).

## Routes are file-declared

The route table is built from the `proxyd_routes` block of each package's
`services/<name>.yaml` plus a static core-route slice in
`compose/compose.go` (`coreProxydRoutes` — dashd, webd, davd, onbod).
`compose.go` collects the survivors after `gated_by` env filtering and
`arizuko generate` writes them into `proxyd_routes` as rows owned by
`arizuko` (`store.SyncProxydRoutes`). proxyd reads the table per request
and dispatches via longest-prefix match.

Sources of truth:

- Core routes: `compose/compose.go` → `coreProxydRoutes`
- Per-service routes: `template/services/<name>.yaml` (copied into
  the data dir by `arizuko packages add`)

Bespoke handling for `/dav/` (group-scoped routing + davAllow write-block)
lives in `dispatchRoute` in `main.go` — the last path proxyd names. `/chat/`
and `/hook/` are plain rows whose `auth: optional` value carries everything
proxyd contributes to them (below).

Routes not in the declared table (hand-wired in `main.go`): `/auth/forward`
(below), `/auth/*` (login flow), `/health`, `/pub/*` (vited fallback +
external redirect), the derived-host `302 → /pub/<world>/` redirect.

See `specs/5/7-proxyd-standalone.md` for the field semantics.

## Runtime route mutation (`/v1/proxyd_routes`)

Operators can add, change, or remove routes at runtime via the
operator-only REST surface. Five endpoints, plus matching MCP tools
(`proxyd_routes.list`, `proxyd_routes.get`, `proxyd_routes.create`,
`proxyd_routes.update`, `proxyd_routes.delete`) surfaced through webd's
`/mcp` bridge. Both faces call the same handler in
`proxyd/resource.go`; the registry lives in `resreg/` (spec 5/7).

The operator page is `/dash/proxyd/` (dashd), which calls these same
endpoints — no third path, and no dashd SQL against `proxyd_routes`.

**Forwarder allowlist.** The stamped `X-User-*` identity is trusted only
on an ES256 service bearer whose subject is in `trustedForwarders`
(`proxyd/resource.go`): `service:webd` (agent MCP tools) and
`service:dashd` (the operator page). It is an allowlist, not "any service
token" — any other valid authd token reaching proxyd directly is 401, or
it could forge operator `X-User-Groups`.

The name is `proxyd_routes`, never `routes` — that one is routd's
message-routing table. A resource's name IS its wire identity, so the
two must not collide (root `CLAUDE.md`).

```
GET    /v1/proxyd_routes
GET    /v1/proxyd_routes/{path}       # path urlencoded, e.g. /v1/proxyd_routes/%2Fslack%2F
POST   /v1/proxyd_routes              # body: {path, backend, auth, gated_by?, preserve_headers?, strip_prefix?}
PATCH  /v1/proxyd_routes/{path}       # body: any subset of the create fields
DELETE /v1/proxyd_routes/{path}       # idempotent (204 either way)
```

**Precedence**: `proxyd_routes` is the only source. proxyd opens routd's
OWNER read/write for `proxyd_routes` and `acl` (`store.OpenOwner(store.OwnerRoutd, …)`
— the endpoint when `DYNAMODB_URL` names one, routd's file otherwise); it does
not own those tables and declares none of them — see "Why proxyd reads routd's
tables directly" below. proxyd never seeds and never reads route config from
env.

Each row carries an `owner`:

- `arizuko` — written by `arizuko generate` from the release's core routes
  plus the installed packages' route assets. **Authoritative**: every
  generate rewrites them, which is how a route added to a release reaches
  an existing instance. `PATCH` on one is refused with 409 — an edit here
  survives only until the next deploy.
- `operator` — created through this API. Generate never touches these, at
  any path.

**Overriding a shipped route**: `DELETE` it, then `POST` your own. The
created row is operator-owned and generate hands the path off for good.
Runtime mutations are visible immediately (no row cache, spec 5/8).

**`auth` values**: `public` (forward as-is), `optional` (identify a caller who
offers a bearer, forward everyone else anonymously — either way with proxyd's
transit proof), `user` / `operator` (bounce to `/auth/login` without one).
`optional` is what `/chat/` and `/hook/` carry: the route token is the
credential, and a stamped sub only decides message attribution.

**Authorization**: operator-only surface. The ACL gate is an empty scope
(`auth.Authorize(caller, "proxyd_routes.<action>", "", nil)`) matched by an
operator ACL row like `(google:op, '*', '**')`. The `**` marker in
X-User-Groups is recorded into `Claims["operator"]="1"` for predicate
matching. Non-operators have no matching row and no mcp:\* tier fallback,
so the call is denied (spec 5/17, spec 5/32 §"Operator implicit").

## Forward auth (`GET /auth/forward`)

The subrequest surface for a proxy that fronts arizuko — Caddy
`forward_auth`, Traefik `ForwardAuth`, nginx `auth_request`
(`specs/6/22` step 3). It answers 200 plus `auth.TransitHeaders` (identity
and the `service:proxyd` bearer that makes a backend trust it), or the same
303-to-login a direct request gets, with `auth_return` set to the caller's
route from `X-Forwarded-Uri`.

`decideForwardAuth` runs `requireAuth`'s attempts in `requireAuth`'s order
rather than deriving its own, so the endpoint cannot admit anyone the
in-process gate refuses. One divergence, recorded rather than hidden: a
refresh needs a `ResponseWriter` to re-set the cookie and a subrequest has
no upstream response to attach one to, so an expired access token takes the
login flow — one round trip more, no difference in who is admitted.

The decision is never cached; the proof is the bearer proxyd already holds,
which bounds a captured answer to that bearer's remaining life
(SECURITY.md §"Handing the proof to a fronting proxy").

`arizuko proxy-config <instance> --forward-auth <host:port>` renders the
route table as a Caddyfile pointed at this endpoint; without the flag every
authenticated route renders as a 503 rather than an open hop.

## Why proxyd reads routd's tables directly

routd owns `acl`; the standing rule is that another daemon
reaches an owner's rows over the owner's HTTP face. proxyd is the one named
exception, decided 2026-08-07 (spec
[`5/16`](../specs/5/16-mcp-rest-unification.md) §"The hot-path exception"):
**it holds a reader on the owner's tables, and reads them on the per-request
hot path.**

One cross-owner read remains and qualifies: `auth.UserScopes` via
`groupsForSub` on every ES256-authenticated request. Route-token resolution is
webd's, which holds a reader on the same tables anyway, so the row has one
reader (specs/6/22 step 1). The alternative considered and rejected was
not an HTTP hop but trusting the login-time `Scope` snapshot in the JWT, which would
let a revoked grant keep working until the caller's next token refresh (15
minutes, `accessTTL`). Reading `acl` per request is what makes revocation take
effect on the next request. `proxyd/hotpath_read_test.go` pins both halves.

The exception does not generalize to "a daemon holding a reader may read
anything" — dashd holds one too, and its audit page moved to HTTP because it
is not on a per-request path.

## WebDAV write-block

Paths under `/dav/` reach `dufs` only after a `davAllow` check on top
of the group-scoped routing:

- Any path under `<group>/logs/` is read-only (read methods: `GET`,
  `HEAD`, `OPTIONS`, `PROPFIND`).
- Sensitive segments are write-blocked anywhere in the path: `.env`,
  any `*.pem`, any `.git` (so a workspace `git/` clone, the agent's
  `.env`, and TLS keys can't be exfiltrated/overwritten via WebDAV).

Blocked requests log `"dav blocked"` and return `403 Forbidden`.

## Entry points

Binary: `proxyd/main.go`, listen on `$PROXYD_LISTEN` (default `:8080`).

Endpoints:

- `/health` — healthcheck
- `/auth/*` — redirects to authd
- `/pub/*` — public files (vited or external redirect)
- `/priv/*` — auth-gated private files (vited)
- `/dav/*` — WebDAV (auth-gated, group-scoped, write-blocked)
- `/v1/proxyd_routes` — operator REST + MCP resource (runtime route mutation)
- `/dash/proxyd/` — proxyd's own operator dashboard, read and forms (spec 6/30)
- `/auth/forward` — the subrequest surface for a fronting proxy
- Manifest-declared routes (e.g. `/dash/`, `/api/`; from `<name>.yaml` + the core slice)

## Dependencies

- `auth` (ES256 JWT verify against authd's JWKs), `chanlib`, `core`, `store`, `resreg`

## Configuration

- `PROXYD_LISTEN` (default `:8080`)
- `VITE_ADDR` (default `http://vited:8080`) — backend for `/pub/*`, `/priv/*`
- `AUTH_SECRET` — keys the OAuth CSRF state; unset means `requireAuth` denies,
  because the login flow cannot complete
- `AUTHD_URL` — authd endpoint for ES256 JWKs + `/auth/*` redirects
- `AUTHD_SERVICE_KEY` — proxyd's service key for ES256 bearer minting
- `AUTHD_SERVICE_NAME` (default `proxyd`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; bare IPs get `/32` or `/128`
- `HOSTING_DOMAIN` — derive world vhosts from `<world>.<HOSTING_DOMAIN>`
- `WEB_VHOST_ALIASES` — override vhost→world mapping (spec 5/V)
- `PUB_REDIRECT_URL` — optional. When set + probe succeeds, `/pub/*` returns `302` to
  `<url>/<rest>` (path + query preserved). Websocket upgrades and probe failures fall
  back to local `vited` proxy. HEAD probe has 2s timeout; result cached 30s.

## Health signal

`GET /health` returns `{"ok":true}` with status 200. Auth failures surface
as 401 (then 303 redirect to `/auth/login`); anon route-token rate limiting
as 429.

## Observability

The two HTTP metrics every daemon emits, at `GET /metrics` under
`METRICS_ENABLED=true`; proxyd adds none of its own (`obs/README.md`).

## Files

- `main.go` — config, path routing, auth gate, identity stamping, vhost derivation
- `routes.go` — `Route`, the in-memory table and its matcher
- `resource.go` — `/v1/proxyd_routes` REST + MCP resource, store-backed route CRUD
- `dash.go` — `/dash/proxyd/`: the route table and its create/delete forms
- `forward_auth.go` — `decideForwardAuth`, the subrequest verdict

## Coverage

`go test ./proxyd/ -short -cover` reports 68.8% (2026-09-13). The gap is one
shape: boot and the mutating dashboard forms.

- `main`, `loadConfig`, `newServer`, `loadInitialRoutes`, `routes`,
  `newPubRedirect`, `defaultProbe` — the wiring that listens on a port and
  exits non-zero. Exercised by `make test-e2e`.
- `handleDashRouteCreate` / `handleDashRouteDelete` and their two helpers —
  the `/dash/proxyd/` forms. The same mutations ARE covered through
  `/v1/proxyd_routes` (`resource_test.go`), which is the handler they call;
  what is untested is the form parse in front of it.

Routing, the auth gate, identity stamping, vhost derivation, the WebDAV
write-block and forward auth are covered.

## Related docs

- `ARCHITECTURE.md` (Web Channel, Auth Hardening)
- `SECURITY.md`
