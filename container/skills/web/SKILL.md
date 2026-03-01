# Web

Deploy web apps by writing files to /web/<app_name>/.
Any directory with index.html is served by vite MPA.

## Creating an app

1. Write files to `/web/myapp/` (index.html required)
2. App is live at `https://$WEB_HOST/myapp/` (if WEB_HOST set)
3. Vite handles TypeScript, CSS, hot reload natively

## Stack

- Vite MPA (no build step needed)
- Vanilla HTML + CSS + JS/TS
- Shared assets in `/web/assets/` (hub.css, hub.js)

## Styling

Use shared CSS variables from hub.css:
`--accent`, `--bg`, `--fg`, `--card`, `--border`, `--dim`

For richer apps: Tailwind CSS via CDN, Alpine.js via CDN.

## Hub page

Root `/web/index.html` lists all deployed apps.
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
