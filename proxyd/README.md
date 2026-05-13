# proxyd

Reverse proxy: public `/pub/`, auth-gated everything else.

## Purpose

Single entry point for web traffic. Handles authentication (JWT +
refresh-token cookie), rate limits slink POSTs, routes by path prefix to
`webd`, `dashd`, `vited`, `onbod`, `davd`. Signs forwarded identity
headers with an HMAC secret shared with `webd`.

## Responsibilities

- Authenticate: `Authorization: Bearer <jwt>` → `refresh_token` cookie → redirect `/auth/login`.
- Inject `X-User-Sub`, `X-User-Groups`, signature (`PROXYD_HMAC_SECRET`).
- Route by TOML-declared prefix table (see "Routes are TOML-declared").
- Rewrite `X-Forwarded-*` from `TRUSTED_PROXIES` CIDRs only.
- Poll `web/vhosts.json` every 5s for hostname → world routing (`specs/4/18-web-vhosts.md`).

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

Bespoke handling for `/slink/` (rate limiter + token resolver) and
`/dav/` (group-scoped routing + davAllow write-block) lives in
`dispatchRoute`'s switch in `main.go`.

Routes not in the TOML table (hand-wired in `main.go`): `/auth/*`
(login flow), `/health`, `/pub/*` (vited fallback + external redirect),
vhost host-header rewriting.

See `specs/6/2-proxyd-standalone.md` for the field semantics.

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

- Binary: `proxyd/main.go`
- Listen: `$PROXYD_LISTEN` (default `:8080`)

## Dependencies

- `auth` (JWT verify, policy), `chanlib`, `core`, `store`

## Configuration

- `PROXYD_LISTEN`, `VITE_ADDR`
- `PROXYD_ROUTES_JSON` — JSON list of routes (see above)
- `AUTH_SECRET`, `AUTH_BASE_URL`, `PROXYD_HMAC_SECRET`
- `TRUSTED_PROXIES` — comma-separated CIDRs
- `PUB_REDIRECT_URL` — optional. When set, `/pub/*` returns `302` to
  `<url>/<rest>` (path + query preserved) instead of being proxied to
  `vited`. A `HEAD` probe (2s timeout) gates the redirect; result is
  cached 30s. If the probe fails, the request falls through to the
  local `vited` proxy — no 502s. Websocket upgrades on `/pub/`
  always use the local proxy. Unset = no redirect, current
  behaviour.

## Health signal

`GET /health` returns 200 when running. Auth failures and slink rate
limiting surface as 401/429 in access logs.

## Files

- `main.go` — config, path routing, auth gate, HMAC signing
- `routes.go` — TOML route struct + `PROXYD_ROUTES_JSON` loader + match/forward

## Related docs

- `ARCHITECTURE.md` (Web Channel, Auth Hardening)
- `SECURITY.md`
