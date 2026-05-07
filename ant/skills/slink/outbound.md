# Slink — outbound (this ant talking to another ant)

An ant can talk to any other ant that has a public slink token — no
shared infrastructure required. Two transports, same token:

| Transport | Path                      | Best for                                  |
| --------- | ------------------------- | ----------------------------------------- |
| HTTP      | `/slink/<token>`          | Simple send + await reply; scripts        |
| MCP       | `/slink/<token>/mcp`      | Tool-shaped calls; multi-step workflows   |

## HTTP transport (fetch + SSE)

The same pattern used by a browser chat page, run from inside an agent
script or scheduled task:

```js
// bun / node
const TOKEN = '<their-slink-token>'
const BASE  = 'https://<their-web-host>'

// 1. Send message → turn handle
const r = await fetch(`${BASE}/slink/${TOKEN}`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ content: 'hello, can you summarise X?' }),
})
const { turn_id } = await r.json()

// 2. Poll for completion (simple)
let done = false
while (!done) {
  const s = await fetch(`${BASE}/slink/${TOKEN}/${turn_id}/status`).then(r => r.json())
  if (s.status !== 'pending') { done = true; break }
  await new Promise(r => setTimeout(r, 2000))
}

// 3. Read reply frames
const snap = await fetch(`${BASE}/slink/${TOKEN}/${turn_id}`).then(r => r.json())
const reply = snap.frames.filter(f => f.kind === 'message').map(f => f.content).join('\n')
```

Or stream the reply via SSE instead of polling:

```js
// EventSource only available in browser; use eventsource npm pkg in Node/Bun
import EventSource from 'eventsource'
const es = new EventSource(`${BASE}/slink/${TOKEN}/${turn_id}/sse`)
es.addEventListener('message', e => process.stdout.write(JSON.parse(e.data).content))
es.addEventListener('round_done', () => { es.close(); process.exit(0) })
```

For Python:

```python
import httpx, sseclient

token, base = '<token>', 'https://<host>'
r = httpx.post(f'{base}/slink/{token}', json={'content': 'hello'})
turn_id = r.json()['turn_id']

with httpx.stream('GET', f'{base}/slink/{token}/{turn_id}/sse') as r:
    for event in sseclient.SSEClient(r.iter_lines()):
        if event.event == 'round_done': break
        print(event.data)
```

## MCP transport (tool calls)

Register the other ant's slink as an MCP server in your Claude Code
settings (or in an agent's `settings.json`):

```json
{
  "mcpServers": {
    "their-ant": {
      "type": "http",
      "url": "https://<their-web-host>/slink/<their-token>/mcp"
    }
  }
}
```

Three tools become available:

| Tool           | What it does                                           |
| -------------- | ------------------------------------------------------ |
| `send_message` | Start a fresh round; returns `{turn_id, topic}`        |
| `steer`        | Append a follow-up to an existing round                |
| `get_round`    | Read reply frames; `wait: true` blocks up to 5 min     |

See `slink-mcp` skill for full reference.

## When to use which

- **HTTP** — one-off messages from a script or scheduled task; when you
  need a quick answer and control over the polling loop.
- **MCP** — multi-step conversations where the other ant's tools are
  first-class; when you want `get_round(wait: true)` without writing
  a poll loop yourself.

## Identity

Both transports are anonymous from the receiving ant's perspective —
sender identity is `anon:slink-<hash>`. There is no way to pass a
signed JWT through a slink call. If per-user attribution matters,
use the JWT-gated `/api/` surface instead.
