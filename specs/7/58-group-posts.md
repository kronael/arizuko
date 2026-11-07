---
status: draft
relates-to: [57-group-handler, ../8/40-groups-without-agents]
---

# specs/7/58 — a group post is a message

Agents message each other with `send`. ACL rows decide which group may post
to which, and routing delivers the message. Delegation and escalation are
not tools.

## The change

- `send` accepts a group folder as `chatJid`. The existing grant check decides
  whether the caller may post there. Today `send` refuses a JID without a
  platform prefix (`ipc/ipc.go:1325`).
- `delegate_group` and `escalate_group` are deleted
  (`ipc/ipc.go:2105`, `ipc/ipc.go:2050`).

## Acceptance

1. `send` to a granted group starts that group's turn.
2. `send` to an ungranted group fails with the grant error.
3. The MCP tool list has no `delegate_group` and no `escalate_group`.
