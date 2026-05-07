# Slink — inbound (others talking to this ant)

## What users get by default

`GET /slink/$SLINK_TOKEN/chat` — minimal anonymous chat page.
Messages arrive in this group like any channel message. Sender identity
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
const TOKEN = '$SLINK_TOKEN' // bake in at page-write time, never pass raw

async function send(msg) {
  // 1. POST → get turn handle
  const r = await fetch(`/slink/${TOKEN}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content: msg }),
  })
  const { turn_id } = await r.json()

  // 2. Stream reply frames
  const es = new EventSource(`/slink/${TOKEN}/${turn_id}/sse`)
  es.addEventListener('message', e => {
    const { content } = JSON.parse(e.data)
    appendToThread('assistant', content)
  })
  es.addEventListener('round_done', () => es.close())
  // Browser auto-reconnects via Last-Event-Id on network drop
}
```

Same-origin page (`$WEB_HOST`) — no CORS needed. Third-party domains:
webd emits `Access-Control-Allow-Origin: *` on all `/slink/*` responses.

## Serving at a custom path

Use `set_web_route` to expose the page at a memorable path:

```
set_web_route path="/chat/" access="public" redirect_to="/pub/<name>/"
```

`set_web_route` controls only paths not hardcoded in proxyd.
`/slink/*` is hardcoded — you cannot redirect it via web_routes.
Link to your page from the channel or from the default chat page.

## Bootstrap endpoint

`GET /slink/<token>/config` — useful when you can't bake the token at
page-write time:

```js
const { name, endpoints } = await fetch(`/slink/${TOKEN}/config`).then(r => r.json())
// endpoints.post, endpoints.stream, endpoints.status
```

Returns: `{ token, folder, name, endpoints: { post, stream, status } }`.
No auth required.
