---
name: web
description: Deploy web apps by writing files to /workspace/web/pub/<app>/. Use when asked to build, create, or deploy a web app or page.
---

# Web

Deploy web apps by writing files to /workspace/web/pub/<app_name>/.
Any directory with index.html is served by vite MPA.

## Access model

- `/pub/` — publicly accessible, no login required
- Everything else — requires authentication

ALWAYS place web apps under `/workspace/web/pub/<app>/`.
NEVER write to `/workspace/web/<app>/` directly (requires auth).

## Creating an app

1. Write files to `/workspace/web/pub/myapp/` (index.html required)
2. App is live at `https://$WEB_HOST/pub/myapp/` (if WEB_HOST set)
3. Vite handles TypeScript, CSS, hot reload natively

## Stack

- Vite MPA (no build step needed)
- Vanilla HTML + CSS + JS/TS
- Shared assets in `/home/node/web/pub/assets/` (hub.css, hub.js)

## Styling

Use shared CSS variables from hub.css:
`--accent`, `--bg`, `--fg`, `--card`, `--border`, `--dim`

For richer apps: Tailwind CSS via CDN, Alpine.js via CDN.

## Hub page

Root `/home/node/web/pub/index.html` lists all deployed apps.
Update it when adding/removing apps.
Never list placeholders or examples.

## Restart vite

If vite crashes or needs restart:

```bash
kill $(cat /srv/app/tmp/vite.pid)
```

The entrypoint auto-restarts vite within ~1s.

## Post-deploy validation

1. Fetch the affected URL (WebFetch or curl)
2. If error or timeout: kill vite PID
3. Wait 2s, verify again before reporting done
