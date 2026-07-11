---
status: shipped
---

Note: `/approve` and `/reject` are wired as graceful stubs; a HITL queue
is a separate spec.

# Control Chat

Operator communication via the operator's own chat. There is no
dedicated root group or `CONTROL_JID` — an operator (a holder of the `**`
grant) issues control commands from any chat they own, recognized by the
existing command registry.

## Design

The operator's own chat is the control chat. Messages follow normal
routing. `/new`, `/stop`, `/ping`, `/chatid`, `/status`, `/root` are
intercepted before container run. Non-command messages proceed to the
agent normally.

## Notifications

`notify/notify.go`. Any service imports to send operator messages to
the operator's JIDs. Looks up the operator's JIDs from routes, sends via channel adapter
HTTP API, records via `store.PutMessage` with `source: "control"` and
`is_bot_message=1`.

Senders: `gated` (container errors, channel health).

## Commands

| Command   | Service | How           | Notes                                        |
| --------- | ------- | ------------- | -------------------------------------------- |
| `/status` | gated   | gated command | Gateway state, channels, containers          |
| `/root`   | gated   | gated command | Raise this turn to root (operator `**` only) |
| `/grant`  | ipc     | MCP tool      | `ipc/grants`, not a chat command             |

Operator-only commands check the `**` grant (`IsOperator`) inside their handler.

## Gaps

- `/status` command wiring (see `d-dashboards.md`)
- `/approve` / `/reject` wiring

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (see `c-audit-log.md`)
- Bot command menus (telegram setMyCommands etc.)
