---
status: partial
shipped: phase-1 (per-daemon route declarations) in v0.35.0; phase-3 (runtime route resource + MCP) in v0.36.0
---

# proxyd: standalone authenticating gateway

> **Status (2026-08-06).** Partial. The resource is registered as
> `proxyd_routes`, so the wire surface is `/v1/proxyd_routes` and the derived
> MCP tools are `proxyd_routes.*`. The dashd surface now exists —
> `/dash/proxyd/` views, adds and deletes routes through that REST face, and
> the cockpit tile is `Built:true` (definition-of-done item 6, closed
> 2026-08-06). Two items remain: the operator component page still documents
> `/v1/routes` and `routes.*`, which is `routd`'s name (item 5 — BUGS `F11`),
> and no migration file / `MIGRATION_VERSION` bump has shipped for it
> (item 7).

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
- **Boot config vs runtime mutation.** Boot routes arrive as one
  `PROXYD_ROUTES_JSON` env var; runtime changes go through the
  `proxyd_routes` resource. No file-watch, no hot-reload of declarations.
  **Persistence wins:** once the `proxyd_routes` table has any row, the env
  var stops being authoritative, so operator mutations survive restart
  (`proxyd/main.go:238` `loadInitialRoutes`).
- **Rate limiting is in-memory only.** Multi-instance horizontal scaling
  would trigger a redis-backed swap; not v1.
- **Audience enforcement** via `require_audience` per route; default is
  "any aud signed by our key is OK".

## Per-daemon route declarations

Routes live beside each adapter's compose fragment as
`template/services/<name>-routes.json`; the compose generator collects
them, evaluates `gated_by` against the operator's `.env`, drops disabled
routes, and emits the survivors as one `PROXYD_ROUTES_JSON` env var
(`compose/compose.go:284,991`).

**One renderer, many sinks**: each daemon owns its routing entry next to
its package, and proxyd's table is derived, never hand-edited. Routes stay
out of the compose fragment itself because proxyd takes ONE env var — a
single assembled table, not per-service YAML
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
file. Either home honours the invariant: **proxyd reads
`PROXYD_ROUTES_JSON` and the `proxyd_routes` table, nothing else.**

## Management surface

The route table is a resreg resource named `proxyd_routes` — **never
`routes`**, which is routd's message-routing table (name collision fixed
`aab3487a`; see CLAUDE.md "A resource's name IS its wire identity").

- REST: `/v1/proxyd_routes` on proxyd (`proxyd/resource.go`).
- MCP: webd forwards to proxyd's REST face (`webd/routes_mcp.go`) — the
  cross-daemon forwarder shape of [`17-openapi-mcp.md`](17-openapi-mcp.md),
  `Store: nil`, so proxyd writes the single audit row.
- Operator: `/dash/proxyd/` (`dashd/proxyd_page.go`) calls the same REST face.
  No dashd SQL against `proxyd_routes` and no dashd `audit.Emit` — proxyd
  writes the one row, in the mutation's own tx, naming the forwarded operator.

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
- `proxyd/routes.go` — `PROXYD_ROUTES_JSON` parsing.
- `proxyd/resource.go` — the `proxyd_routes` resreg resource.
- `compose/compose.go` — collects `*-routes.json`, emits the env var.
- `auth/` — mint + verify primitives proxyd consumes.
