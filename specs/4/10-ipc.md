---
status: shipped
---

# ipc

> **Note.** The MCP socket is now hosted **in-process by routd**
> (`ipc.ServeMCP` called from `routd.ServeTurnMCP`), not by a standalone
> `gated.sock` daemon — see [`specs/5/E-routd.md`](../5/E-routd.md). The
> tool surface and socket-path identity model below are unchanged.

`ipc/` package: one per-group MCP server on a unix socket. It accepts
connections from agent containers, stamps each request with the
caller's identity, and executes the tool inline — handlers call
routd/store/timed functions directly. There is no forwarding to
another daemon.

## Identity is a parameter, not a derivation

`ipc.ServeMCP` takes the caller's `folder` as an argument
(`ipc/ipc.go`). One socket serves exactly one group, and the spawning
daemon — which already knows which folder it spawned — says which.

An earlier design parsed the folder back out of the socket path
(`/ipc/<folder>/gated.sock`). That is the reverse-engineering pattern
root `CLAUDE.md` bans outright: a path is a deployment detail, and
reading identity out of one gets you the wrong answer the first time
the layout moves.

Capability attached to that identity is the ACL's business, not this
layer's — [`../5/32-acl-unified.md`](../5/32-acl-unified.md) for the row model and
[`../5/33-paths-roles.md`](../5/33-paths-roles.md) for the path/role/grant-option
axes that replaced folder-depth tiers.

## MCP server

One unix socket per group. Agent containers connect via
socat bridge:

```json
{
  "arizuko": {
    "command": "socat",
    "args": ["STDIO", "UNIX-CONNECT:/run/ipc/router.sock"]
  }
}
```

Authorization is checked per call before execution.

## Tools

The tool inventory is **not** listed here — it drifts. Cold-tier
management tools are derived from `resreg` resources
([`../5/17-openapi-mcp.md`](../5/17-openapi-mcp.md), rollout in
[`../5/16-mcp-rest-unification.md`](../5/16-mcp-rest-unification.md));
the current MCP face is enumerated in
[`../5/E-routd.md`](../5/E-routd.md). Only hot-tier agent actions
(`reply`/`send`/`inspect_*`) stay hand-authored in `ipc/`.

Two handler contracts below are recorded here because each fixed a
production failure and neither is recoverable from the code alone.

### SpawnGroup contract

The callback is `SpawnGroup(parentFolder, childJID string)`. The
caller passes **its own folder** as `parentFolder` rather than a JID
to look up. Earlier versions took `(parentJID, childJID)` and resolved
`parentJID → folder` through the routes table, which silently failed
whenever the calling agent's own JID had no default route — the common
case, since a child learns about itself from its spawn context, not
from a route. Same lesson as the identity rule above: pass what you
know, do not look it up.

### Route mutation safety

`delete_route` and `set_routes` MUST refuse to remove the caller's own
`Seq == 0` route — the folder's primary inbound route. The guard is
against self-harm, not against malice: an agent chasing adapter-routing
502s once deleted its own default route, leaving its JID unrouted and
falling back into onboarding. It could not be reached to be told.

`Seq == 0` is the convention for that primary route in the collapsed
routes table; matching is by the route's `match` expression, not by a
`type` column. Callers may still delete routes they own but did not
originate. Live in `routd/routes_resource.go`; folded into the shared
resreg handler per [`../5/16-mcp-rest-unification.md`](../5/16-mcp-rest-unification.md).

## No tables owned

ipc is stateless — no tables, no migrations. Every read goes through
an injected callback.

## Layout

```
ipc/
  ipc.go
  inspect.go
  README.md
  SECURITY.md
```
