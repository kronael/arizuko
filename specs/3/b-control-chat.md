---
status: shipped
---

Note: `/approve` and `/reject` are wired as graceful stubs; a HITL queue
is a separate spec.

# Control Chat

Operator communication via root group's chat. No dedicated `CONTROL_JID`
— root's JIDs from the routing table are the control channel. Commands
use the existing command registry.

## Design

Root = control chat. Messages follow normal routing. `/new`, `/stop`,
`/ping`, `/chatid`, `/status`, `/root` intercepted by gated before
container run. Non-command messages proceed to root agent normally.

## Notifications

`notify/notify.go`. Any service imports to send operator messages to
root's JIDs. Looks up root's JIDs from routes, sends via channel adapter
HTTP API, records via `store.PutMessage` with `source: "control"` and
`is_bot_message=1`.

Senders: `gated` (container errors, channel health).

## Commands

| Command   | Service | How           | Notes                               |
| --------- | ------- | ------------- | ----------------------------------- |
| `/status` | gated   | gated command | Gateway state, channels, containers |
| `/root`   | gated   | gated command | Delegate to root group              |
| `/grant`  | ipc     | MCP tool      | `ipc/grants`, not a chat command    |

Root-only commands check tier inside their handler.

## Gaps

- `/status` command wiring (see `d-dashboards.md`)
- `/approve` / `/reject` wiring

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (see `c-audit-log.md`)
- Bot command menus (telegram setMyCommands etc.)
