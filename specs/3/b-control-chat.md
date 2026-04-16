---
status: draft
---

# Control Chat

**Status**: shipped (partial)

Operator communication via root group's chat.
No dedicated `CONTROL_JID` — root's JIDs from routing table
are the control channel. Commands use existing command registry,
not a separate dispatcher.

## Design

Root group is the control chat. Messages to root follow normal
routing. `/new`, `/stop`, `/ping`, `/chatid`, `/status`, `/root`
are intercepted by gated before container run. Non-command
messages proceed to root agent normally.

## Notifications (shared library)

`notify/` package. Any service imports it to send operator
messages to root's JIDs. Looks up root's JIDs from routes
table, sends via channel adapter HTTP API, records via
`store.PutMessage` with `source: "control"` and `is_bot_message=1`.

Note: `notify/` package ships in `notify/notify.go`.

Senders: `gated` (container errors, channel health).

## Commands

| Command   | Service | How           | Notes                               |
| --------- | ------- | ------------- | ----------------------------------- |
| `/status` | gated   | gated command | Gateway state, channels, containers |
| `/root`   | gated   | gated command | Delegate to root group              |
| `/grant`  | ipc     | MCP tool      | `ipc/grants`, not a chat command    |

Root-only commands check tier inside their handler.
`/grant` is an MCP tool, not a route.

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (covered by audit-log spec)
- Bot command menus (telegram setMyCommands etc.)
