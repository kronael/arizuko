# Slink — inbound (others talking to this ant)

## Endpoints

| Method | Path                                      | What it does                             |
| ------ | ----------------------------------------- | ---------------------------------------- |
| GET    | `/slink/<token>`                          | Browser chat UI (HTML page)              |
| POST   | `/slink/<token>`                          | Send message → `{user, turn_id, status}` |
| POST   | `/slink/<token>/<turn_id>`                | Steer — follow-up to an existing round   |
| GET    | `/slink/<token>/<turn_id>`                | Snapshot: status + all assistant frames  |
| GET    | `/slink/<token>/<turn_id>?after=<msg_id>` | Cursor: frames after `<msg_id>`          |
| GET    | `/slink/<token>/<turn_id>/status`         | Cheap status check (no frame payload)    |
| GET    | `/slink/<token>/<turn_id>/sse`            | SSE stream until `round_done`            |
| POST   | `/slink/<token>/mcp`                      | MCP tool surface (agent-to-agent)        |

## Rate limits

| Caller            | Limit                         |
| ----------------- | ----------------------------- |
| Anonymous         | 10 req/min (shared per token) |
| JWT-authenticated | 60 req/min per user           |
| Agent / operator  | unlimited                     |

## Default page

`GET /slink/$SLINK_TOKEN` — minimal anonymous chat page. Sender identity
is an IP-derived hash (`anon:<hex>`); no account required.

Share it:

```
send_message content="Chat with me: https://$WEB_HOST/slink/$SLINK_TOKEN"
```

## Build a custom chat page

Deploy to `/workspace/web/pub/<name>/index.html` — served at
`https://$WEB_HOST/pub/<name>/` with no auth.

Full pattern — fetch + EventSource, zero dependencies:

```js
const TOKEN = '$SLINK_TOKEN' // resolve at page-write time, never pass the literal variable

async function send(msg) {
  const r = await fetch(`/slink/${TOKEN}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: msg }),
  })
  const { turn_id } = await r.json()

  const es = new EventSource(`/slink/${TOKEN}/${turn_id}/sse`)
  es.addEventListener('message', e => {
    const { content } = JSON.parse(e.data)
    appendToThread('assistant', content)
  })
  es.addEventListener('round_done', () => es.close())
}
```

Page lives on the same origin as `$WEB_HOST` — no CORS issues.

## Serving at a custom path

```
set_web_route path="/chat/" access="public" redirect_to="/pub/<name>/"
```

`set_web_route` controls only paths not hardcoded in proxyd.
`/slink/*` is hardcoded — cannot be redirected via web_routes.
