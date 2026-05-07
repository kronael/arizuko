---
name: slink
description: Ant links — share this ant's public chat URL, build a custom
  chat page for it, or send messages to another ant via their slink token.
  Use when sharing your URL, building a web chat UI, or doing agent-to-agent
  communication over HTTP or MCP.
---

# Slink

Two directions:

- **Inbound** — others reach this ant via a public URL.
  Read `/workspace/self/ant/skills/self/slink-inbound.md`.

- **Outbound** — this ant sends messages to another ant via their slink.
  Read `/workspace/self/ant/skills/self/slink-outbound.md`.

## Endpoints (same token, different paths)

| Method | Path                                      | What it does                              |
| ------ | ----------------------------------------- | ----------------------------------------- |
| GET    | `/slink/<token>`                          | Browser chat UI (HTML page)               |
| POST   | `/slink/<token>`                          | Send message → `{user, turn_id, status}`  |
| POST   | `/slink/<token>/<turn_id>`                | Steer — follow-up to an existing round    |
| GET    | `/slink/<token>/<turn_id>`                | Snapshot: status + all assistant frames   |
| GET    | `/slink/<token>/<turn_id>?after=<msg_id>` | Cursor: frames after `<msg_id>`           |
| GET    | `/slink/<token>/<turn_id>/status`         | Cheap status check (no frame payload)     |
| GET    | `/slink/<token>/<turn_id>/sse`            | SSE stream until `round_done`             |
| POST   | `/slink/<token>/mcp`                      | MCP tool surface (agent-to-agent)         |

## Rate limits

| Caller            | Limit                         |
| ----------------- | ----------------------------- |
| Anonymous         | 10 req/min (shared per token) |
| JWT-authenticated | 60 req/min per user           |
| Agent / operator  | unlimited                     |
