---
name: web
description: >
  Deploy web apps, pages, sites, dashboards to your group's web slot —
  `~/public_html/` (public) or `~/private_html/` (OAuth/JWT). USE for
  "build a page", "deploy this site", "publish a dashboard", "show my
  web", static sites, index.html, single-page apps. NOT for
  howto/getting-started (use howto), knowledge hubs (use hub), or REST
  APIs (use service).
user-invocable: true
---

# Web

Deploy by writing files to `~/public_html/<app>/`. Any directory with
`index.html` is served by vite MPA. The bind mount projects each path
into the unified `<data>/web/pub/<your-folder>/` tree.

## Two slots, two URL spaces

| Slot              | URL                                          | Auth      |
| ----------------- | -------------------------------------------- | --------- |
| `~/public_html/`  | `https://$WEB_HOST/pub/<your-folder>/...`    | none      |
| `~/private_html/` | `https://$WEB_HOST/priv/<your-folder>/...`   | OAuth/JWT |

`https://$WEB_HOST/pub/<X>` (public) and `https://$WEB_HOST/<X>`
(JWT-rewrite) serve the SAME file. `https://$WEB_HOST/priv/<X>`
serves a DIFFERENT file from a separate filesystem tree.

## Other web URL prefixes

- `/pub/*` — public, no login (your `~/public_html/`)
- `/priv/*` — JWT, OAuth-gated (your `~/private_html/`)
- `/dash/`, `/chat/`, `/api/`, `/x/` — JWT-gated (not static)
- `/auth/*` — OAuth flow

ALWAYS place public apps under `~/public_html/<app>/`. ALWAYS place
OAuth-gated apps under `~/private_html/<app>/`. NEVER write to
`/workspace/web/...` — that path is gone (v0.45.11).

## Create a public app

1. `mkdir -p ~/public_html/myapp && cat > ~/public_html/myapp/index.html`
2. Live at `https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/myapp/`
3. Vite handles TypeScript, CSS, hot reload natively

## Create an OAuth-gated app

1. `mkdir -p ~/private_html/admin && cat > ~/private_html/admin/index.html`
2. Live at `https://$WEB_HOST/priv/$ARIZUKO_GROUP_FOLDER/admin/`
3. Requires a logged-in user with JWT (operator + invited users)

## Stack

- Vite MPA (no build step)
- Vanilla HTML + CSS + JS/TS
- Shared assets: link to `/pub/arizuko/assets/hub.css` (operator-curated docs)
- Rich apps: Tailwind CDN, Alpine CDN

## Browse what other groups publish

`/var/lib/www/` mounts the whole public web tree read-only. To see
what's already published or link to operator-curated docs:

```bash
ls /var/lib/www/
# link in your HTML:
# <link rel="stylesheet" href="/pub/arizuko/assets/hub.css">
```

You cannot write to `/var/lib/www/` directly (tier 1+). Your writable
view is `~/public_html/`.

## Nested subgroups

A tier-2 group `atlas/support` has `~/public_html/` projecting to
`<data>/web/pub/atlas/support/` — URL: `/pub/atlas/support/...`.
The parent group `atlas` SEES this subdir as RO via
`/var/lib/www/atlas/support/`. Treat subgroup directory names as
reserved.

## Hub page

Root `~/public_html/index.html` lists deployed apps for your folder.
Update when adding/removing apps.

## Web chat

Each group has a route token for public chat at
`https://$WEB_HOST/chat/<token>/`. Mint via `issue_chat_link`. For a
custom chat page inside your web slot, use the round-handle JSON API.

## Post-deploy

Fetch the URL (WebFetch or curl) to verify before reporting done.
`curl -sI https://$WEB_HOST/pub/$ARIZUKO_GROUP_FOLDER/myapp/` MUST
return 200.
