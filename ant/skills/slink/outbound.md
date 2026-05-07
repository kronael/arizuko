# Slink — outbound (this ant talking to another ant)

An ant can talk to any other ant that has a public slink token — no
shared infrastructure required. Two transports, same token:

| Transport | Path                      | Best for                                |
| --------- | ------------------------- | --------------------------------------- |
| HTTP      | `/slink/<token>`          | Simple send + await reply; scripts      |
| MCP       | `/slink/<token>/mcp`      | Tool-shaped calls; multi-step workflows |

## HTTP transport

```js
// bun / node
const TOKEN = '<their-slink-token>'
const BASE  = 'https://<their-web-host>'

// 1. Send message → turn handle
const { turn_id } = await fetch(`${BASE}/slink/${TOKEN}`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ content: 'hello' }),
}).then(r => r.json())

// 2a. Poll for completion
let snap
do {
  await new Promise(r => setTimeout(r, 2000))
  snap = await fetch(`${BASE}/slink/${TOKEN}/${turn_id}`).then(r => r.json())
} while (snap.status === 'pending')

const reply = snap.frames.filter(f => f.kind === 'message').map(f => f.content).join('\n')
```

Stream instead of polling (use `eventsource` npm pkg — EventSource is browser-only):

```js
import EventSource from 'eventsource'
const es = new EventSource(`${BASE}/slink/${TOKEN}/${turn_id}/sse`)
es.addEventListener('message', e => process.stdout.write(JSON.parse(e.data).content))
es.addEventListener('round_done', () => { es.close(); process.exit(0) })
```

Python:

```python
import httpx, sseclient

token, base = '<token>', 'https://<host>'
turn_id = httpx.post(f'{base}/slink/{token}', json={'content': 'hello'}).json()['turn_id']

with httpx.stream('GET', f'{base}/slink/{token}/{turn_id}/sse') as r:
    for event in sseclient.SSEClient(r.iter_lines()):
        if event.event == 'round_done': break
        print(event.data)
```

## MCP transport

See `slink-mcp` skill — full tool reference and registration instructions.

## When to use which

- **HTTP** — one-off messages, scripts, scheduled tasks; full control over
  the poll/stream loop.
- **MCP** — multi-step conversations; `get_round(wait: true)` blocks without
  a poll loop.

## Identity

Both transports are anonymous to the receiving ant — sender is
`anon:slink-<hash>`. For per-user attribution use the JWT-gated `/api/` surface.
