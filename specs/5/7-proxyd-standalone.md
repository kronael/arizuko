---
status: shipped
shipped: phase-1 (per-daemon route declarations) in v0.35.0; phase-3 (runtime route resource + MCP) in v0.36.0; dashd surface + operator docs 2026-08-06
---

# proxyd: standalone authenticating gateway

`proxyd` is a generic, config-driven authenticating reverse proxy —
droppable in front of any HTTP service stack, arizuko or otherwise. Same
blueprint as `auth`: clean library boundary, HTTP API for management, MCP
surface for agents that manage routes.

## Decisions

- **No mint mode.** `authd` is the sole signer
  ([`1-auth-standalone.md`](1-auth-standalone.md)); proxyd only verifies
  offline against `authd`'s cached JWKs and delegates login + mint. The
  earlier `[auth].mode = library|remote` toggle is withdrawn — there is no
  in-process-mint mode, and proxyd's old local HS256 mint was transitional,
  deleted in the release `authd` landed.
- **proxyd is the enforcement point; `auth`/`authd` is the authority.**
  proxyd does authn + simple authz + path routing. It is not a layer-7
  firewall, not a service mesh (backends are static URLs, no discovery, no
  internal mTLS), not a TLS terminator (something upstream handles TLS),
  and not a token issuer.
- **The table is the only config.** `proxyd_routes` in routd.db is where a
  route lives; proxyd reads it per request and reads no route config from
  env. `arizuko generate` writes the release's routes into it
  (`store.SyncProxydRoutes`), runtime changes go through the `proxyd_routes`
  resource. No file-watch, no hot-reload of declarations.
  Routes used to arrive as a `PROXYD_ROUTES_JSON` env var that proxyd read
  only while the table was empty — once it had a row the env var was ignored
  for the life of the instance, so seven routes arizuko shipped never reached
  krons and every health check stayed green (BUGS `L45`). Generated env
  config stops applying and looks healthy; a row does not.
- **Ownership decides who rewrites a row.** `proxyd_routes.owner` is
  `arizuko` (written by generate; **authoritative** — every generate rewrites
  it, and PATCH on one answers 409) or `operator` (created through the
  resource; generate never touches it, at any path). Editing an
  arizuko-owned row is not an override — taking a shipped path over is
  DELETE then POST, and the row you create is operator-owned. Generate
  writes only paths it ships, so an operator route at any other path is
  never read, rewritten or deleted.
- **Rate limiting is in-memory only.** Multi-instance horizontal scaling
  would trigger a redis-backed swap; not v1.
- **Audience enforcement** via `require_audience` per route; default is
  "any aud signed by our key is OK".

## Per-daemon route declarations

Routes live beside each adapter's compose fragment as
`template/services/<name>-routes.json`; the compose generator collects
them, evaluates `gated_by` against the operator's `.env` and drops disabled
routes; `Generate` returns the survivors and `arizuko generate` writes them
into `proxyd_routes` (`compose/compose.go` `collectProxydRoutes`,
`cmd/arizuko/routes.go` `syncProxydRoutes`).

**One renderer, many sinks**: each daemon owns its routing entry next to
its package, and proxyd's table is derived, never hand-edited. Routes stay
out of the compose fragment itself because proxyd, not docker, is their
consumer — one assembled table, not per-service YAML
([`27-compose-native-packaging.md`](27-compose-native-packaging.md)). v1
ships a plain JSON array; the `{"routes":[…]}` envelope was dropped during
implementation as redundant.

Shape: `template/services/slakd-routes.json` (the only shipped example).
Parser: `proxyd/routes.go`.

### Field semantics

- **`path`** — exact match without a trailing slash, longest-prefix match
  with one. No glob, no regex. Among prefix matches longest wins; ties go
  to load order (filename sort).
- **`backend`** — full URL, daemon DNS name + `:8080` per the unified-port
  convention (CLAUDE.md). No service-mesh resolution.
- **`auth`** — `public` (proxyd does nothing; the daemon itself verifies,
  e.g. Slack HMAC over the raw body), `user` (valid session required;
  proxyd stamps `X-User-*`), or `operator` (operator/admin role).
- **`gated_by`** — a single env-var name. Unset or empty at
  compose-generate time drops the route entirely, so the path 404s. This
  replaced per-adapter `if envOr(env, "SLACK_BOT_TOKEN", "") != ""` blocks
  in `compose.go`. Compound gates are out of scope.
- **`preserve_headers`** — explicit allowlist of inbound headers proxyd
  MUST pass through unmodified. `httputil.ReverseProxy` already preserves
  headers, but proxyd strips its own headers on entry; this is the contract
  that webhook-signing headers survive any future filtering pass. proxyd
  otherwise rewrites `Host` to the backend's hostname.
- **`strip_prefix`** — default `false`. Webhook URLs keep their prefix.

### Identity headers

On an `auth: user` route proxyd stamps `X-User-Sub` / `X-User-Name` /
`X-User-Groups` and attaches a `service:proxyd` ES256 bearer minted by
`authd`. **Backends trust the stamped headers only on that transit proof**
(`auth.ProxydTransit`, `auth/middleware.go:12`); they verify the bearer,
never a per-request signature.

The HMAC-era `X-User-Sig` is retired: proxyd deletes any inbound value
(`proxyd/main.go:821`) and never sets one — the service bearer is the
channel proof. Full trust model in `SECURITY.md` §"Identity header trust".

### Out of scope (v1)

No verb allowlist (the WebDAV write-block stays a davd-specific in-proxyd
check); no per-route `rate_limit` (slink's limiter stays hardcoded); no
`token_param` (slink URL-token resolution stays bespoke); single-env-var
`gated_by` only.

Core daemons rendered directly by `compose.go` (dashd, webd, davd, onbod,
vited) contribute their routes from `compose.go` rather than a declaration
file. Either home honours the invariant: **proxyd reads the
`proxyd_routes` table, nothing else.**

## Management surface

The route table is a resreg resource named `proxyd_routes` — **never
`routes`**, which is routd's message-routing table (name collision fixed
`aab3487a`; see CLAUDE.md "A resource's name IS its wire identity").

- REST: `/v1/proxyd_routes` on proxyd (`proxyd/resource.go`).
- MCP: webd forwards to proxyd's REST face (`webd/routes_mcp.go`) — the
  cross-daemon forwarder shape of [`17-openapi-mcp.md`](17-openapi-mcp.md),
  `Store: nil`, so proxyd writes the single audit row. **Operator-only, and
  not on the agent socket**: `registerRoutesMCP` returns early for
  non-operators, and `ipc.addFacadeTools` skips every resource without
  `MCPNames` — `proxyd_routes` among them. The in-container agent never lists
  these tools.
- Operator: `/dash/proxyd/` (`dashd/proxyd_page.go`) calls the same REST face.
  No dashd SQL against `proxyd_routes` and no dashd `audit.Emit` — proxyd
  writes the one row, in the mutation's own tx, naming the forwarded operator.
- Docs: `template/web/pub/arizuko/components/proxyd.html` (operator page),
  `proxyd/README.md` (package), `reference/openapi.html` (the `proxyd_routes`
  row).

**Forwarder allowlist.** proxyd trusts stamped `X-User-*` only on an ES256
service bearer whose sub is in `trustedForwarders` (`proxyd/resource.go`):
`service:webd` and `service:dashd`. It stays an allowlist — any other valid
authd token reaching proxyd directly is 401, or it could forge operator
groups. Both forwarders authenticate the end user themselves before
translating them onto this API.

Session management (`/v1/sessions` list + force-logout) needs a revocation
list and is deferred with the auth spec's revocation work.

## Open (post-v1)

- **Slink-style URL tokens** — generalising the bespoke path-token
  resolution to a `token_param` route flag. The rate-limit and
  folder-resolution logic factors out cleanly but adds schema surface v1
  did not need.
- **Standalone documentation** — `proxyd/README.md` needs a "drop me in
  front of your stack" section with example configs before the component
  is genuinely reusable outside arizuko.

## Code pointers

- `proxyd/main.go` — entry, route dispatch, identity stamping.
- `proxyd/routes.go` — the in-memory `Route` shape + longest-prefix match.
- `proxyd/resource.go` — the `proxyd_routes` resreg resource.
- `compose/compose.go` — collects `*-routes.json`, returns the route table.
- `store/proxyd_routes.go` — `SyncProxydRoutes`, the generate-time writer.
- `auth/` — mint + verify primitives proxyd consumes.
