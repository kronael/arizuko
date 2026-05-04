---
status: shipped
---

# proxyd — web proxy layer

Public-facing HTTP daemon. Owns the external port, validates all
credentials, injects identity headers, routes to internal daemons
which trust headers unconditionally.

Routing (in order):

1. `WEB_REDIRECTS` JSON prefix map to arbitrary upstreams
2. `/auth/*` handled locally (login, OAuth, logout, refresh)
3. `/pub/*` → `WEBD_ADDR` (or `VITE_ADDR` fallback), public
4. `/dash/*` → `DASH_ADDR`, auth-gated
5. `/slink/*` → `WEBD_ADDR`, public (token resolved at proxyd)
6. `/*` → auth-gated; unauth redirects to `/auth/login`

`/pub/` zone maps to `/workspace/web/pub/<app>/` per group.
`GET /` and `GET /pub` → 302 to `/pub/`. Fail-closed when
`AUTH_SECRET` empty — private routes unreachable, `/pub` + `/auth`
still work.

vhosts: `vhosts.json` hot-reloaded every 5s; matching `Host` rewrites
to `/<world>/<path>` via viteProxy regardless of prefix.

Web channel is a separate adapter (`webd/`), registered via chanreg
like any other.

`vited` = Vite dev server (`arizuko-vite` image) used when `WEBD_ADDR`
unset; internal only.
