---
status: superseded-in-part
superseded-in-part: [5/33-paths-roles]
---

# MCP tool authorization

A folder names an agent. ACL rows decide which actions the agent may call
and which target folders those actions may reach. Folder depth confers no
permission. [Paths and roles](../5/33-paths-roles.md) defines the model.

## Group messages require send on the target

`send(chatJid="<folder>", text="...")` posts a message to that group.
The gate checks `mcp:send` on the target folder. The target can be a parent,
child or peer. The message transfers no grant and carries no depth argument.

`auth/defaults.go` declares the default actions for `role:member`, including
`send`. Local ACL rows can restrict the target. `register_group` requires its
own grant to create a group.

## Posts carry a return address

`store.PostGroup` writes the message. `ForwardedFrom` records the calling
folder for an agent's `send`, or the originating chat for a routing hand-off.
An agent sends a response to another group with `send` and that group's full
folder as `chatJid`.
