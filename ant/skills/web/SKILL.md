---
name: web
description: Deploy web apps, pages, sites, dashboards, or UIs to /workspace/web/pub/.
when_to_use: Use when asked to build, create, deploy, publish, make, or show anything web-facing.
---

# Web

Deploy by writing files to `/workspace/web/pub/<app>/`. Any directory with
`index.html` is served by vite MPA.

## Access

- `/pub/*` — public, no login
- `/dash/`, `/chat/`, `/api/`, `/x/` — JWT-gated
- `/slink/*` — token-gated anonymous chat
- `/auth/*` — OAuth flow
- Any other path redirects to `/pub/` + path (then 404 if missing)

ALWAYS place apps under `/workspace/web/pub/<app>/`. NEVER write to
`/workspace/web/<app>/` — it won't be served. Paths like `/sub/` or
`/private/` do not exist.

## Create an app

1. Write files to `/workspace/web/pub/myapp/` (`index.html` required)
2. Live at `https://$WEB_HOST/pub/myapp/`
3. Vite handles TypeScript, CSS, hot reload natively

## Stack

- Vite MPA (no build step)
- Vanilla HTML + CSS + JS/TS
- Shared assets in `/workspace/web/pub/assets/` (`hub.css`, `hub.js`)
- Rich apps: Tailwind CDN, Alpine CDN

## Hub page

Root `/workspace/web/pub/index.html` lists deployed apps. Update when
adding/removing apps. No placeholders.

## Web chat (slink)

Each group has a slink token for public chat at
`https://$WEB_HOST/slink/$SLINK_TOKEN`. To build a custom chat page
inside a pub app, use the round-handle JSON API — see `slink-inbound` skill
for the full pattern and a working snippet.

## Vite restart

Vite runs in a separate `vited` container with `restart: on-failure`. Restart
from the host if needed.

## Post-deploy

Fetch the URL (WebFetch or curl) to verify before reporting done.
