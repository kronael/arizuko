---
name: slink-mcp
description: >
  MCP transport over a slink token — `POST /slink/<token>/mcp`. USE
  for "external MCP", "drive my group from outside", "agent-to-agent
  call", slink token setup, cross-ant tool calls. NOT for one-off
  messages (prefer HTTP) or internal MCP socket (use mcp skill).
user-invocable: true
---

# Slink MCP

`POST /slink/<token>/mcp` — stateless MCP over HTTP. Token is auth.

## Three tools

| Tool           | Purpose                                                            |
| -------------- | ------------------------------------------------------------------ |
| `send_message` | Inject a fresh user message; starts a new round. Returns `{turn_id, topic, folder}`. |
| `steer`        | Append a follow-up to an existing round (same topic). For mid-round clarifications. |
| `get_round`    | Read assistant replies for a topic. `wait: true` blocks up to 5min for the next reply (server-capped). |

## Auth

The token IS the auth — possessing it equals group membership. No
JWT, no signed headers. Treat slink tokens as bearer credentials.
Sender identity is anon-derived (`anon:slink-<short-hash>`); MCP
callers don't bring their own identity to the group.

Code: `webd/slink_mcp.go`.
