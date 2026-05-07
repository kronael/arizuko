---
name: slink
description: Ant links (slinks) — public token-gated web chat for your
  group. Use when sharing your chat URL, explaining how someone can reach
  you on the web, or describing the MCP surface for external agents.
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

## What users get

Visiting `https://$WEB_HOST/slink/$SLINK_TOKEN` opens a minimal web
chat UI — anonymous, monospace, dark/light theme. Messages route into
your group exactly like any channel message. Identity is an IP-derived
anonymous hash (`anon:<hex>`); senders have no account.

## Sharing

Send the URL to anyone you want to reach you publicly:

```
send_message content="Chat with me: https://$WEB_HOST/slink/$SLINK_TOKEN"
```

Or post it to a platform — it's just a URL.

## Round-handle API (for power users / scripts)

Three endpoints, same token:

| Method | Path | What it does |
| ------ | ---- | ------------ |
| `POST` | `/slink/<token>` | Send a message; returns HTML bubble or JSON |
| `GET` | `/slink/<token>/<turn_id>` | Snapshot of a turn's frames |
| `GET` | `/slink/<token>/<turn_id>/sse` | SSE stream until round completes |

POST body: `content=hello&topic=<optional-id>`.
GET `Accept: application/json` for machine-friendly output.

## MCP surface (agent-to-agent)

External agents connect via `POST /slink/<token>/mcp` — three tools:

| Tool | Purpose |
| ---- | ------- |
| `send_message` | Start a new round; returns `{turn_id, topic}` |
| `steer` | Append to an existing round |
| `get_round` | Read replies; `wait: true` blocks up to 5 min |

The token IS the auth. See `ant/skills/slink-mcp/SKILL.md` for full reference.

## SSE stream (basic embed)

```js
const es = new EventSource(
  `/slink/stream?token=${token}&group=${folder}&topic=${topic}`)
es.addEventListener('message', e => {
  const { id, role, content } = JSON.parse(e.data)
})
```

Events: `{ id, role, content, created_at }`. Topic auto-generated if omitted.

## Rate limits

Proxyd rate-limits per IP. Anonymous users share the same limit pool.
