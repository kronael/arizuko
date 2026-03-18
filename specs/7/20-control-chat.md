# Control Chat

**Status**: design

Operator communication via root group's chat.
No dedicated `CONTROL_JID` — root's JIDs from routing table
are the control channel. Commands use existing command registry,
not a separate dispatcher.

## Design

Root group is the control chat. Messages to root follow normal
routing. `/new`, `/stop`, `/ping`, `/chatid` are intercepted
by gated before container run. `/approve` and `/reject` are
prefix routes in the routing table pointing to `onbod` —
they never reach gated's command handler. Non-command
messages proceed to root agent normally.

## Notifications (shared library)

`notify/` package. Any service imports it to send operator
messages. Same pattern as auth — shared library, not
duplicated code.

```go
// notify/notify.go
func Send(db *store.Store, text string) error
```

1. Look up root's JIDs from routes table (folder = "root")
2. Send to each via channel adapter HTTP API
3. Record via `store.StoreOutbound(source: "control")`

### Who sends what

| Service | Notifications                                       |
| ------- | --------------------------------------------------- |
| `onbod` | Onboarding events ("New: alice via telegram:-1234") |
| `gated` | Container errors, channel health                    |
| `dashd` | None (read-only)                                    |

## Commands

| Command    | Service        | How                        | Notes                               |
| ---------- | -------------- | -------------------------- | ----------------------------------- |
| `/status`  | gated or dashd | gated command (TBD: route) | Gateway state, channels, containers |
| `/approve` | onbod          | route → onbod service      | Approve pending onboard             |
| `/reject`  | onbod          | route → onbod service      | Reject pending onboard              |
| `/grant`   | ipc            | MCP tool                   | `ipc/grants`, not a chat command    |

Root-only commands check tier inside their handler.
`/approve` and `/reject` are routing table entries, not
`gateway/commands.go` handlers. `/status` may follow
(TBD). `/grant` is an MCP tool, not a route.

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (covered by audit-log spec)
- Bot command menus (telegram setMyCommands etc.)
