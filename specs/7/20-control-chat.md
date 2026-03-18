# Control Chat

**Status**: design

Operator communication via root group's chat.
No dedicated `CONTROL_JID` — root's JIDs from routing table
are the control channel. Commands use existing command registry,
not a separate dispatcher.

## Design

Root group is the control chat. Messages to root follow normal
routing. Gateway commands (`/status`, `/approve`, etc.) are
intercepted before container run — same as `/new`, `/stop`,
`/ping`. Non-command messages proceed to root agent normally.

## Notifications (shared library)

`notify/` package. Any service imports it to send operator
messages. Same pattern as authd — shared library, not
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

| Command    | Service        | Notes                                         |
| ---------- | -------------- | --------------------------------------------- |
| `/status`  | gated or dashd | TBD                                           |
| `/approve` | onbod          | Approve pending onboard                       |
| `/reject`  | onbod          | Reject pending onboard                        |
| `/grant`   | icmcd          | MCP tool (`icmcd/grants`), not a chat command |

Root-only commands check tier inside their handler.
Registered in `gateway/commands.go` like existing commands
(except `/grant` which is an MCP tool).

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (covered by audit-log spec)
- Bot command menus (telegram setMyCommands etc.)
