---
name: web
description: Deploy web apps, pages, sites, dashboards, or UIs. Use when asked to build, create, deploy, publish, make, or show anything web-facing. ALWAYS write to /workspace/web/pub/<app>/index.html — NEVER to /home/node/.
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
- Shared assets in `/workspace/web/pub/assets/` (hub.css, hub.js)

## Styling

Use shared CSS variables from hub.css:
`--accent`, `--bg`, `--fg`, `--card`, `--border`, `--dim`

For richer apps: Tailwind CSS via CDN, Alpine.js via CDN.

## Hub page

Root `/workspace/web/pub/index.html` lists all deployed apps.
Update it when adding/removing apps.
Never list placeholders or examples.

## Vite restart

Vite runs in a separate `vited` Docker container with `restart: on-failure`.
Restart from the host if needed.

## Web chat (slink)

Each group has a slink token for public web chat. Endpoints:

- `POST /slink/<token>` — send message (form: `content`, `topic`)
- `GET /slink/stream?group=<folder>&topic=<t>` — SSE response stream

When users ask about web chat, point them to
`https://$WEB_HOST/slink/<token>`. Authenticated UI at `/chat/<folder>`.

### Usage with curl

```bash
# send a message (returns HTML fragment)
curl -X POST https://host/slink/TOKEN -d "content=hello&topic=t1"

# stream responses (SSE, keep-alive)
curl -N https://host/slink/stream?group=FOLDER&topic=t1
# each event: {"id","role","content","created_at"}
```

### Embedding in apps

Slink is a plain HTTP API — no SDK needed. Send = POST form,
receive = SSE EventSource. Minimal JS integration:

```js
// send
fetch(`/slink/${token}`, {
  method: 'POST',
  body: new URLSearchParams({ content: msg, topic: tid })
})
// receive
const es = new EventSource(
  `/slink/stream?group=${folder}&topic=${tid}`)
es.addEventListener('message', e => {
  const { content, role } = JSON.parse(e.data)
})
```

Topic is auto-generated if omitted. Reuse the same topic for
a conversation thread. Anonymous users are identified by IP hash.

## Post-deploy validation

Fetch the affected URL (WebFetch or curl) to verify before reporting done.
