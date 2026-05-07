---
name: slink
description: Ant links (slinks) — token-gated web channel for a group. Use
  when sharing your chat URL, building a custom chat page, reaching another
  ant via their slink, or describing the MCP surface for external agents.
---

# Slink

Two directions, same primitive:

- **Inbound** — others reach this ant via a public URL.
  Read `/workspace/self/ant/skills/slink/inbound.md`.

- **Outbound** — this ant sends messages to another ant via their slink.
  Read `/workspace/self/ant/skills/slink/outbound.md`.

## Quick reference

```bash
echo "https://$WEB_HOST/slink/$SLINK_TOKEN"  # this ant's link
```

NEVER output literal variables. Resolve before sharing.
If `$SLINK_TOKEN` is empty, web chat is not configured for this group.

## Endpoints (same token, different paths)

| Method | Path                            | What it does                        |
| ------ | ------------------------------- | ----------------------------------- |
| POST   | `/slink/<token>`                | Send message → `{turn_id, status}`  |
| GET    | `/slink/<token>/config`         | Bootstrap: token, folder, name      |
| GET    | `/slink/<token>/chat`           | Default browser chat page           |
| GET    | `/slink/<token>/<turn_id>`      | Snapshot: status + all reply frames |
| GET    | `/slink/<token>/<turn_id>/sse`  | SSE stream until `round_done`       |
| GET    | `/slink/<token>/<turn_id>/status` | Cheap status check                |
| POST   | `/slink/<token>/<turn_id>`      | Steer — follow-up to existing turn  |
| POST   | `/slink/<token>/mcp`            | MCP tool surface (agent-to-agent)   |

## Rate limits

| Caller            | Limit                        |
| ----------------- | ---------------------------- |
| Anonymous         | 10 req/min (shared per token)|
| JWT-authenticated | 60 req/min per user          |
