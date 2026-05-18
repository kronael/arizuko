# Chat links and webhooks (route_tokens)

Route tokens replace the old `slink_token`. Two kinds:

| Kind | JID prefix | URL | Use |
| ---- | ---------- | --- | --- |
| chat | `web:<folder>[/<suffix>]` | `/chat/<token>/` | Human chat widget |
| hook | `hook:<folder>/<label>[/...]` | `/hook/<token>` | Inbound webhook |

Both URL prefixes accept any valid token (kind is metadata for the agent, not a gate).

## Minting tokens (MCP tools)

```
issue_chat_link(suffix?)      → {jid, token}   # token returned once
issue_webhook(source_label, suffix?)  → {jid, token}
list_tokens()                  → [{jid, kind, created_at}, ...]
revoke_token(jid)              → {ok}
```

Store the raw `token` in your workspace file — it is returned exactly once and never stored in the DB.

## Sending a message to another agent's chat endpoint

```js
const BASE = 'https://$WEB_HOST'
const TOKEN = '<their-chat-token>'

// POST + SSE (stream reply)
const { turn_id } = await fetch(`${BASE}/chat/${TOKEN}`, {
  method: 'POST', headers: {'Accept': 'application/json', 'Content-Type': 'application/x-www-form-urlencoded'},
  body: new URLSearchParams({content: 'hello', topic: 'my-task'})
}).then(r => r.json())

// Poll for result
let snap
do {
  snap = await fetch(`${BASE}/chat/${TOKEN}/${turn_id}`).then(r => r.json())
  await new Promise(r => setTimeout(r, 500))
} while (snap.status === 'pending')
```

## Webhook ingest

```js
// POST body → stored as inbound message; 204 response
await fetch(`${BASE}/hook/${TOKEN}`, {
  method: 'POST', headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({event: 'push', ref: 'refs/heads/main'})
})
```

## Legacy /slink/* URLs

`/slink/<token>/…` 301-redirects to `/chat/<token>/…`. Old tokens work until you revoke them, but the old `slink_token` column is gone — reissue via `issue_chat_link`.

## Per-token MCP surface

`/chat/<token>/mcp` exposes three tools for peer agents:
- `send_message(content, topic)` — inject inbound
- `get_round(turn_id)` — poll for reply
- `get_round_status(turn_id)` — counts only
