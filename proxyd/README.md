# proxyd

Reverse proxy + OAuth gateway + user-session token issuer.

## Purpose

Single entry point for web traffic. Authenticates users (OAuth dance,
JWT session, refresh-token cookie), mints the user's session token at
login, rate limits slink POSTs, routes by path prefix to `webd`,
`dashd`, `vited`, `onbod`, `davd`. Signs forwarded identity headers
with an HMAC secret shared with `webd`.

## Responsibilities

- Authenticate: `Authorization: Bearer <jwt>` → `refresh_token` cookie → redirect `/auth/login`.
- OAuth dance: `/auth/login`, `/auth/callback`, `/auth/logout` (GitHub, Google, Discord, Telegram widget).
- Mint user session JWT at login completion (see "Token issuance" below).
- Inject `X-User-Sub`, `X-User-Groups`, signature (`PROXYD_HMAC_SECRET`).
- Route by prefix: `/pub/*` public, `/dash/*` → dashd, `/dav/*` → dufs, `/slink/*` rate-limited, `/api/*` and `/x/*` and `/chat/*` → webd, `/*` → webd.
- Rewrite `X-Forwarded-*` from `TRUSTED_PROXIES` CIDRs only.
- Poll `web/vhosts.json` every 5s for hostname → world routing (`specs/4/18-web-vhosts.md`).

## Token issuance

proxyd is one of three issuers of platform tokens (proxyd at OAuth login,
MCP host at agent spawn, onbod at invite redemption). All three call
into the shared `auth/` library and produce the same JWT shape — backend
verifiers don't care which issued, only that the signature checks.

**Today (shipped):** OAuth completes → proxyd issues a session JWT
carrying `sub`, `iat`, `exp`. Trust is also propagated to backends via
HMAC-signed identity headers (`X-User-Sub`, `X-User-Groups`) — backends
verify with `auth.RequireSigned` / `StripUnsigned`.

**Per `specs/6/7-platform-api.md` (planned):** proxyd's minter moves to
`auth.Mint(...)` and embeds the full token shape:

```
sub    "user:<oidc-subject>"
scope  ["groups:read", "tasks:write", ...]   computed from grants table at issuance
folder "*" for tier 0/1; scoped subtree for lower tiers
tier   denormalized from grants for fast tier-gated checks
iat,exp,iss="proxyd"
```

The wire transport stays `Authorization: Bearer <jwt>`; signed identity
headers persist through cutover and retire once every backend uses
`auth.VerifyHTTP`. If a centralized `authd` daemon is added later,
proxyd's minting moves there as a refactor — the OAuth flow stays here.

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

- `auth` (JWT mint/verify, OAuth, HMAC, policy), `chanlib`, `core`, `store`

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

- `specs/6/7-platform-api.md` — token model + federated `/v1/*` contract
- `auth/README.md` — shared mint/verify primitives
- `ARCHITECTURE.md` (Web Channel, Auth Hardening)
- `SECURITY.md`
