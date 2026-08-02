---
status: shipped
---

# Control chat

**There is no control chat.** That is the decision: no dedicated root
group, no `CONTROL_JID`, no special channel. An operator — anyone
holding the `**` grant — issues control commands from any chat they
already own, and the existing command registry recognizes them.

A dedicated control JID would have been a second routing path to keep
alive, a second place for auth to drift, and a single point of failure
for exactly the situation you need it in (something is broken). Routing
operator commands through normal routing means the control surface is
tested by every ordinary message.

## Commands

`/new`, `/stop`, `/ping`, `/chatid`, `/status`, `/root` are intercepted
before the container runs; everything else proceeds to the agent
normally. Operator-only commands (`/root` raises the turn to root)
check the `**` grant inside their own handler rather than at a gate, so
the check sits next to the privilege it protects.

`/grant` is an MCP tool, not a chat command — grants are a management
resource with both faces
([`../5/16-mcp-rest-unification.md`](../5/16-mcp-rest-unification.md)).

## Notifications — not currently wired

This spec described a `notify` library that looked up the operator's
JIDs from routes and recorded messages with `source: "control"`. The
library was inlined into its single caller (the gateway) as a
single-caller cleanup, and the gateway was deleted at v0.50.0. No
`source: "control"` writer exists today, and grepping for one returns
nothing — operator-facing failures surface through journald and the
dashd status pages instead. Rebuild this deliberately if push
notification is wanted again; do not assume it is live.

`/approve` and `/reject` are wired as graceful stubs — a HITL queue is
[`../5/19-hitl-firewall.md`](../5/19-hitl-firewall.md) (draft).

## Not in scope

Multi-operator role separation, an audit log of control commands
([`c-audit-log.md`](c-audit-log.md)), platform command menus
(`setMyCommands`).
