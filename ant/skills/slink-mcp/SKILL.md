---
name: slink-mcp
description: Reference for the slink-bound MCP transport — `POST
  /slink/<token>/mcp`, distinct from the SSE web chat widget. Use when
  setting up an external agent (Claude Code or similar) to drive an
  arizuko group, or when explaining why slink chat ≠ slink MCP.

---

# Slink MCP

Two surfaces, one token. A slink token grants a single group's chat
membership; the route prefix decides which transport the caller gets:

| Path                  | Transport         | Audience                           |
| --------------------- | ----------------- | ---------------------------------- |
| `/slink/<token>/`     | HTML + SSE        | Web chat widget (browser users)    |
| `/slink/<token>/mcp`  | MCP over HTTP     | External agents                    |

Same token, same group, different framing. Web chat manages a UI
session and message history; MCP is stateless and tool-shaped.

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

## When to reach for it

- Multi-agent handoff: Agent A pastes Agent B's `/slink/<token>/mcp`
  URL into its MCP server config, then drives B's group with
  `send_message` + `get_round`.
- Programmatic control of a group from a script outside the container.
- Anywhere the SSE chat widget is overkill and you just want
  request/response semantics over MCP.

## Not for

- Long-running real-time UIs — use the web chat widget at
  `/slink/<token>/` instead. Slink MCP has no message history endpoint.
- Authenticated user identity — every MCP call is anon. If you need
  per-user attribution, use the JWT-gated `/api/` surface.

Code: `webd/slink_mcp.go`.
