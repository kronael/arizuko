---
status: shipped
---

# proxyd — Web Proxy Layer

proxyd is the public-facing HTTP daemon. It owns the external port,
acts as the auth oracle for all downstream services, and routes to
internal daemons.

## Problem

Auth logic was duplicated across dashd and webd. The web channel
question ("include in proxy or separate daemon?") was open. This spec
closes both: proxyd handles all auth at the perimeter; webd is a
separate channel adapter.

## Design

**Renamed from webd.** The original spec called this daemon `webd`.
Renamed to `proxyd` to avoid collision with the web chat channel
adapter (`webd/` — see `specs/6/3-web-chat.md`).

**Auth at the perimeter.** proxyd validates JWT and slink tokens,
injects identity headers, then forwards. Downstream services trust
headers unconditionally — they never re-validate. One auth surface,
one place to change policy.

**Routing** (evaluated in order):

1. `WEB_REDIRECTS` — JSON prefix map to arbitrary upstreams
2. `/auth/*` — handled locally (login, OAuth, logout, refresh)
3. `/pub/*` → `WEBD_ADDR` (or `VITE_ADDR` fallback), **public** (no auth)
4. `/dash/*` → `DASH_ADDR` (dashd), auth-gated
5. `/slink/*` → `WEBD_ADDR`, public (slink token resolved at proxyd)
6. `/*` → auth-gated; on unauth, redirects to `/auth/login` (NOT a 404)

**`/pub/` is the public zone.** Agent-generated websites live under
`/workspace/web/pub/<app-name>/index.html` (host:
`<DATA_DIR>/groups/<folder>/web/pub/<app-name>/`) and are served
without auth. Everything outside `/pub/` requires a valid JWT or
refresh-token cookie.

**Fail-closed on missing `AUTH_SECRET`.** When `AUTH_SECRET` is
empty, `requireAuth` short-circuits to `http.NotFound` — private
routes become unreachable by anyone. `/pub/*` and `/auth/*` still
route normally so the public zone and login page remain available.
This is deliberately fail-closed: earlier logic silently accepted
any token when the secret was empty.

**Root redirect.** `GET /` and `GET /pub` both redirect to `/pub/`
(302 Found). Anonymous visitors landing on `https://<host>/` see
the public zone instead of a redirect loop or login wall.

**Vhosts take precedence.** If `Host` matches an entry in
`vhosts.json` (hot-reloaded every 5s), the request is rewritten to
`/<world>/<path>` and proxied to `viteProxy` regardless of
`/pub/` vs auth-gated path prefixes. Used to mount a whole group's
web dir at a custom hostname.

**Web channel.** Implemented as a separate channel adapter (`webd/`)
registered via chanreg — consistent with teled, discd, etc. proxyd
proxies to it; does not implement the channel logic itself.

## Code

`proxyd/main.go` — config, routing, `requireAuth` middleware,
reverse proxies for dashd/webd/vited. `auth.RegisterRoutes` handles
`/auth/*` locally.

`auth/` — JWT mint/verify, OAuth (Google), login page, session
management (argon2 passwords, refresh tokens in store).

## vited

Vite dev server (`arizuko-vite` image). Used when `WEBD_ADDR` is not
set. Serves `WEB_DIR` as a multi-page app. Internal only — not
exposed outside compose network.
