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
3. `/dash/*` → `DASH_ADDR` (dashd), auth-gated
4. `/slink/*` → `WEBD_ADDR`, public (slink token resolved at proxyd)
5. `/*` → `WEBD_ADDR` (or `VITE_ADDR` fallback), auth-gated unless `WEB_PUBLIC=true`

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
