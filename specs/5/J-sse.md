---
status: planned
---

# SSE Stream — incomplete

## Current behaviour

`GET /slink/stream?group=<folder>` opens an SSE connection. The gateway
broadcasts every agent response to **all listeners** on that group folder,
regardless of who sent the triggering message.

`/slink/` is in `PUBLIC_PREFIXES` — basic auth is bypassed. Anyone who
knows the URL can subscribe to the stream.

This is intentional for the public widget model: `REDACTED.js` embeds on a
public page, all visitors see all agent responses.

## Design direction: groups are the boundary

Groups are the conversational and permission boundary. Per-sender
scoping within a group is the wrong abstraction — it fights the
shared-context model. Instead:

- **Public group** → SSE broadcast to all (current, correct)
- **Private group** → require auth (JWT) on the stream endpoint
- **Per-user isolation** → auto-spawn a group per user via prototypes

This means SSE auth is just "can you access this group." No per-sender
tagging, no sub claims on messages, no filtering in sendMessage.

### Auth on the stream endpoint

Move `/slink/stream` out of `PUBLIC_PREFIXES` when the group requires auth:

- Group has no `AUTH_SECRET` → stream stays open (public widget)
- Group has `AUTH_SECRET` → require valid JWT on stream request
  (`?token=<jwt>` or `Authorization` header)

### Prototypes for per-user groups

When a new authenticated user connects and no dedicated group exists,
gateway auto-spawns from a prototype config. Each spawned group gets
its own folder, session, SSE stream. Auth is inherited from the
prototype's permissions.

See `specs/3/F-prototypes.md` for the prototype spawning design.

## MCP transport context

The current slink pattern (POST for client→server, SSE for server→client) is
structurally identical to the MCP SSE transport defined in spec v2024-11-05.
That transport was deprecated in v2025-03-26 in favour of **Streamable HTTP**:
a single endpoint that handles both directions, with the server optionally
responding via an SSE stream when it wants to push multiple events.

Reference: modelcontextprotocol.io/specification/2025-03-26/basic/transports

### Slink as MCP server (open, v2+)

If the gateway exposed a Streamable HTTP MCP endpoint per group, any MCP
client (Claude Desktop, another agent, a browser widget) could connect
natively without a custom protocol. The agent would appear as an MCP server;
callers would send tool calls and receive streaming results.

Trade-offs:

- No custom REDACTED.js needed for MCP-capable clients
- Agent-to-agent calls work out of the box
- Rate limiting / auth moves to the MCP layer
- Browser widget still needs a thin adapter (MCP client in JS)
- Fire-and-forget POST pattern is lost (MCP is request/response)

Not planned for v1. Document here as design direction.

## Open questions

- How does prototype spawning interact with group limits / cleanup?
- Should spawned groups expire after idle timeout or persist?
- Stream reconnect: replay missed events from DB or accept gap?

## Not in scope

- Presence (who is online)
- Per-sender filtering within a group (use group isolation instead)
