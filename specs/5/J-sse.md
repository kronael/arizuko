---
status: planned
---

# SSE Stream

## Design direction: groups are the boundary

Groups are the conversational and permission boundary. Per-sender
scoping within a group is the wrong abstraction — it fights the
shared-context model:

- **Public group** → SSE broadcast to all listeners (public widget)
- **Private group** → require auth on the stream endpoint
- **Per-user isolation** → auto-spawn a group per user via prototypes

SSE auth is "can you access this group" — nothing more granular.

For the web chat UI specifically, topic scopes delivery within a group:
the SSE hub delivers `message` events by `(folder, topic)`. See
`specs/6/3-web-chat.md` for the slink/SSE auth model.

## Auth on the stream endpoint

- Group has no slink token → stream is open (public widget)
- Group has slink token → require token on stream request

Token validated at proxyd before reaching webd. Not in `PUBLIC_PREFIXES`.

## Prototypes for per-user groups

When a new user connects and no dedicated group exists, gateway
auto-spawns from a prototype config. Each spawned group gets its own
folder, session, SSE stream. Auth inherited from prototype permissions.

See `specs/3/F-prototypes.md`.

## Open questions

- How does prototype spawning interact with group limits and cleanup?
- Should spawned groups expire after idle timeout or persist?

## MCP transport direction (v2+)

The slink pattern (POST client→server, SSE server→client) is
structurally identical to the deprecated MCP SSE transport
(v2024-11-05). The current standard is Streamable HTTP (v2025-03-26):
one endpoint, bidirectional, server responds via SSE when pushing
multiple events.

If the gateway exposed a Streamable HTTP MCP endpoint per group, any
MCP client (Claude Desktop, another agent, a browser widget) could
connect natively. Trade-offs: no custom JS needed for MCP clients;
fire-and-forget POST pattern is lost (MCP is request/response).

Not planned. Document as design direction.

## Not in scope

- Presence (who is online)
- Per-sender filtering within a group (use group isolation instead)
