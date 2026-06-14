# proxyd

Reverse proxy: public `/pub/`, auth-gated everything else.

## Purpose

Single entry point for web traffic. Handles authentication (JWT +
refresh-token cookie), rate limits anon route-token traffic (`/chat/`,
`/hook/`), routes by path prefix to backends. Stamps forwarded identity
headers with a service bearer (`service:proxyd`) verifiable by backends.

## Responsibilities

- Authenticate: `Authorization: Bearer <jwt>` (HS256 or ES256) → `refresh_token` cookie → redirect `/auth/login`.
- Stamp `X-User-Sub`, `X-User-Name`, `X-User-Groups` + `Authorization: Bearer service:proxyd`.
- Route by DB-backed prefix table (runtime mutate via `/v1/routes`).
- Rewrite `X-Forwarded-For` from `TRUSTED_PROXIES` CIDRs only.
- Derive each world's host (`<world>.<HOSTING_DOMAIN>`) and 302 it to
  `/pub/<world>/`; `WEB_VHOST_ALIASES` overrides labels ≠ world name
  (`specs/5/V-web-vhosts.md`).
- Honor agent-registered web_routes (redirect/deny/auth gating on `/pub/*`).

## Routes are TOML-declared

The route table is built from `[[proxyd_route]]` blocks in each
service's `template/services/<name>.toml` plus a static core-route
slice in `compose/compose.go` (`coreProxydRoutes` — dashd, webd, davd,
onbod). `compose.go` collects survivors after `gated_by` env filtering,
serializes to JSON, and injects as `PROXYD_ROUTES_JSON` on proxyd.
proxyd reads the env at startup and dispatches via longest-prefix match.

Sources of truth:

- Core routes: `compose/compose.go` → `coreProxydRoutes`
- Per-service routes: `template/services/*.toml` → `[[proxyd_route]]` blocks

Bespoke handling for `/chat/`, `/hook/` (route-token resolver + anon DoS
shield) and `/dav/` (group-scoped routing + davAllow write-block) lives
in `dispatchRoute`'s switch in `main.go`.

Routes not in the TOML table (hand-wired in `main.go`): `/auth/*`
(login flow), `/health`, `/pub/*` (vited fallback + external redirect),
the derived-host `302 → /pub/<world>/` redirect.

See `specs/5/35-proxyd-standalone.md` for the field semantics.

## Runtime route mutation (`/v1/routes`)

Operators can add, change, or remove routes at runtime via the
operator-only REST surface. Five endpoints, plus matching MCP tools
(`routes.list`, `routes.get`, `routes.create`, `routes.update`,
`routes.delete`) surfaced through webd's `/mcp` bridge. Both faces call
the same handler in `proxyd/resource.go`; the registry lives in
`resreg/` (spec 5/5).

```
GET    /v1/routes
GET    /v1/routes/{path}       # path urlencoded, e.g. /v1/routes/%2Fslack%2F
POST   /v1/routes              # body: {path, backend, auth, gated_by?, preserve_headers?, strip_prefix?}
PATCH  /v1/routes/{path}       # body: any subset of the create fields
DELETE /v1/routes/{path}       # idempotent (204 either way)
```

**Precedence**: routes persist to `proxyd_routes` in messages.db. On first
boot, if the table is empty AND `PROXYD_ROUTES_JSON` is set, proxyd
seeds the table from the env var. Thereafter the table is authoritative
and the env var is ignored. Runtime mutations are visible immediately
(no row cache, spec 5/36) and durable across restarts.

**Authorization**: operator-only surface. The ACL gate is an empty scope
(`auth.Authorize(caller, "routes.<action>", "", nil)`) matched by an
operator ACL row like `(google:op, '*', '**')`. The `**` marker in
X-User-Groups is recorded into `Claims["operator"]="1"` for predicate
matching. Non-operators have no matching row and no mcp:\* tier fallback,
so the call is denied (spec 5/5, spec 4/9 §"Operator implicit").

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
- `/chat/<token>/`, `/hook/<token>` — route-token surfaces
- `/v1/routes` — operator REST + MCP resource (runtime route mutation)
- TOML-declared routes (e.g. `/dash/`, `/api/`)

## Dependencies

- `auth` (JWT verify, ES256/HS256 dual), `chanlib`, `core`, `store`, `resreg`

## Configuration

- `PROXYD_LISTEN` (default `:8080`)
- `VITE_ADDR` (default `http://vited:8080`) — backend for `/pub/*`, `/priv/*`
- `PROXYD_ROUTES_JSON` — JSON list of routes; seeds DB on first boot if table empty
- `AUTH_SECRET` — HS256 JWT verify secret
- `AUTHD_URL` — authd endpoint for ES256 JWKs + `/auth/*` redirects
- `AUTHD_SERVICE_KEY` — proxyd's service key for ES256 bearer minting
- `AUTHD_SERVICE_NAME` (default `proxyd`)
- `TRUSTED_PROXIES` — comma-separated CIDRs; bare IPs get `/32` or `/128`
- `CHAT_ANON_DOS_RPM` (default `10`) — anon IP rate limit on `/chat/`, `/hook/`
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

Metrics emitted when `METRICS_ENABLED=true`:

- `arizuko_requests_total` — HTTP requests (daemon, method, status)
- `arizuko_request_duration_seconds` — request latency (daemon, method, path)

Spec: `specs/5/O-observability.md`.

## Files

- `main.go` — config, path routing, auth gate, identity stamping, vhost derivation
- `routes.go` — Route struct, LoadRoutes, MatchRoute
- `resource.go` — `/v1/routes` REST + MCP resource, DB-backed route CRUD

## Related docs

- `ARCHITECTURE.md` (Web Channel, Auth Hardening)
- `SECURITY.md`
