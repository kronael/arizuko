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
- Route by prefix: `/pub/*` public, `/dash/*` → dashd, `/dav/*` → dufs, `/slink/*` rate-limited, `/*` → webd.
- Rewrite `X-Forwarded-*` from `TRUSTED_PROXIES` CIDRs only.
- Poll `web/vhosts.json` every 5s for per-vhost overrides.

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

- `PROXYD_LISTEN`, `DASH_ADDR`, `WEBD_ADDR`, `DAV_ADDR`, `VITE_ADDR`, `ONBOD_ADDR`
- `AUTH_SECRET`, `AUTH_BASE_URL`, `PROXYD_HMAC_SECRET`
- `TRUSTED_PROXIES` — comma-separated CIDRs

## Health signal

`GET /health` returns 200 when running. Auth failures and slink rate
limiting surface as 401/429 in access logs.

## Files

- `main.go` — config, path routing, auth gate, HMAC signing

## Related docs

- `ARCHITECTURE.md` (Web Channel, Auth Hardening)
- `SECURITY.md`
