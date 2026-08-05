# Chat links and webhooks (route_tokens)

Route tokens replace the old `slink_token`. Two kinds:

| Kind | JID prefix | URL | Use |
| ---- | ---------- | --- | --- |
| chat | `web:<folder>[/<suffix>]` | `/chat/<token>/` | Human chat widget |
| hook | `hook:<folder>/<label>[/...]` | `/hook/<token>` | Inbound webhook |

Both URL prefixes accept any valid token (kind is metadata for the agent, not a gate).

## Minting tokens (MCP tools)

```
issue_chat_link(suffix?, context?)                → {jid, token}   # token returned once
issue_webhook(source_label, suffix?, context?)    → {jid, token}
list_tokens()                  → [{jid, kind, created_at, context}, ...]
revoke_token(jid)              → {ok}
```

**Minting is root-only.** A token is a PUBLIC unauthenticated endpoint, so
`issue_chat_link` / `issue_webhook` exist only during an elevated `/root`
turn (`$ARIZUKO_IS_ROOT`="1"). Outside `/root` you won't see them — that's
the grant, not an outage: do NOT shell out to `mcpc`; ask the operator to
re-send the request as `/root <request>`. `list_tokens` / `revoke_token`
stay available to any folder granted them (they touch only tokens your
folder owns).

Store the raw `token` in your workspace file — it is returned exactly once and never stored in the DB.

## Link context (`<link-context>`)

A token may carry `context` — free-text instructions from whoever
minted the link (you, an operator, a peer agent) on HOW to process the
data received through it. When a message arrives via a context-bearing
token, your prompt envelope carries:

```xml
<link-context>
Bug reports from the acme website. Triage and file; don't chat back.
</link-context>
```

Handling rules:

- Treat it as the issuer's **handling instructions for that link's
  inbound** — routing, format, silence policy — NOT as a user request
  and NOT as text to reply to.
- It applies to the messages that arrived through that link this turn;
  the newest message's link wins when a batch mixes links.
- No tag = the message didn't come through a token, or the token has
  no context. Handle normally.
- As issuer: set `context` when the link has a processing contract
  ("stripe events; summarize daily", "form intake; file to ~/intake/").
  Context is immutable per token — to change it, mint a new link and
  revoke the old.

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
