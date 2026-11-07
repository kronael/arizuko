---
status: draft
relates-to: [57-group-handler, ../8/40-groups-without-agents]
---

# specs/7/58 — a group post is a message

An agent hands work to another group by sending it a message. Delegation and
escalation are a recipe in a skill, not tools.

## The change

- `send` accepts a group folder as `chatJid`. The existing grant check decides
  whether the caller may post there. Today `send` refuses a JID without a
  platform prefix (`ipc/ipc.go:1325`).
- `delegate_group` and `escalate_group` are deleted
  (`ipc/ipc.go:2105`, `ipc/ipc.go:2050`).
- A skill in `ant/skills/self/` tells the agent to include its return address
  in the text, so the other group can answer with `send`.

## Acceptance

1. `send` to a granted group starts that group's turn.
2. `send` to an ungranted group fails with the grant error.
3. The MCP tool list has no `delegate_group` and no `escalate_group`.
