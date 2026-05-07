---
name: slink
description: Ant links (slinks) — public token-gated web chat for your
  group. Use when sharing your chat URL, building a custom chat page,
  explaining how someone can reach you on the web, or describing the MCP
  surface for external agents.
---

# Ant links

An **ant link** is a public, token-gated URL that lets anyone chat with
your group via a browser or programmatic client — no account required.

## Your ant link URL

```bash
echo "https://$WEB_HOST/slink/$SLINK_TOKEN"
```

NEVER output the literal variables. Always resolve before sharing.
If `$SLINK_TOKEN` is empty, web chat is not configured for this group.

## What users get by default

Visiting `https://$WEB_HOST/slink/$SLINK_TOKEN` opens a minimal web
chat UI — anonymous, monospace. Messages route into your group exactly
like any channel message. Identity is an IP-derived anonymous hash
(`anon:<hex>`); senders have no account.

## Sharing

```
send_message content="Chat with me: https://$WEB_HOST/slink/$SLINK_TOKEN"
```

Or post it to a platform — it's just a URL.

## Round-handle JSON API

POST returns JSON by default. No `Accept` header needed.

| Method | Path                          | What it does                          |
| ------ | ----------------------------- | ------------------------------------- |
| POST   | `/slink/<token>`              | Send message → `{turn_id, status}`    |
| GET    | `/slink/<token>/config`       | Bootstrap: token, folder, name        |
| GET    | `/slink/<token>/<turn_id>`    | Snapshot: status + all reply frames   |
| GET    | `/slink/<token>/<turn_id>/sse`| SSE stream until `round_done`         |
| GET    | `/slink/<token>/<turn_id>/status` | Cheap status check               |
| POST   | `/slink/<token>/<turn_id>`    | Steer — follow-up to an existing turn |

## Build a custom chat page

Agents own the UI. Write the page to `/workspace/web/pub/<name>/index.html`
— it's served at `https://$WEB_HOST/pub/<name>/` with no auth.

Full pattern — two browser primitives, zero dependencies:

```js
const TOKEN = '$SLINK_TOKEN' // resolve at page-build time, never pass raw var

async function send(msg) {
  // 1. POST message → get turn handle
  const r = await fetch(`/slink/${TOKEN}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: msg }),
  })
  const { turn_id } = await r.json()

  // 2. Stream reply frames on the turn handle
  const es = new EventSource(`/slink/${TOKEN}/${turn_id}/sse`)
  es.addEventListener('message', e => {
    const { content } = JSON.parse(e.data)
    appendToThread('assistant', content)
  })
  es.addEventListener('round_done', () => es.close())
  // Browser reconnects automatically via Last-Event-Id on network drop
}
```

The page lives on the same origin as `$WEB_HOST` — no CORS headers needed.
For pages hosted on a third-party domain, webd emits
`Access-Control-Allow-Origin: *` on all `/slink/*` responses.

### Serving at a custom path

Deploy to `/workspace/web/pub/<name>/index.html` and share the URL
`https://$WEB_HOST/pub/<name>/`. Use `set_web_route` to expose it at
a memorable path (e.g. `/chat/` → `/pub/<name>/`):

```
set_web_route path="/chat/" access="public" redirect_to="/pub/<name>/"
```

`set_web_route` only controls paths that aren't already hardcoded in proxyd.
`/slink/*` is hardcoded — you cannot redirect it via web_routes.
Link to your page from the default slink chat page or from the channel.

### Bootstrap

`GET /slink/<token>/config` returns the group name and resolved endpoint
paths — useful when you can't bake the token into the HTML at build time:

```js
const { name, endpoints } = await fetch(`/slink/${TOKEN}/config`).then(r => r.json())
// endpoints.post, endpoints.stream, endpoints.status
```

## MCP surface (agent-to-agent)

External agents connect via `POST /slink/<token>/mcp` — three tools:

| Tool | Purpose |
| ---- | ------- |
| `send_message` | Start a new round; returns `{turn_id, topic}` |
| `steer` | Append to an existing round |
| `get_round` | Read replies; `wait: true` blocks up to 5 min |

The token IS the auth. See `slink-mcp` skill for full reference.

## Rate limits

| Caller | Limit |
| ------ | ----- |
| Anonymous | 10 req/min (shared per token) |
| JWT-authenticated | 60 req/min per user |
