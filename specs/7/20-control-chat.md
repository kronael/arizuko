# Control Chat

**Status**: design

Gateway-to-operator communication via root group's chat.
No dedicated `CONTROL_JID` — root's JIDs from routing table
are the control channel. Commands use existing command registry,
not a separate dispatcher.

## Design

Root group is the control chat. Messages to root follow normal
routing. Gateway commands (`/status`, `/approve`, etc.) are
intercepted before container run — same as `/new`, `/stop`,
`/ping`. Non-command messages proceed to root agent normally.

## Gateway to operator (notifications)

`notify(text string)` in `gateway/notify.go`:

- Looks up root's JIDs via route table (folder = "root")
- Sends to each via `HTTPChannel.Send`
- Records via `storeOutbound(source: "control")`

Examples:

- Onboarding: "New: alice via telegram:-12345"
- Errors: "Container timeout for atlas/"
- Health: "Channel discord reconnected after 5m"

## Operator to gateway (commands)

Registered in `gateway/commands.go` like existing commands.
Root-only commands check tier inside their handler.

## Not in scope

- Multi-operator (future — role-based access)
- Audit log of control commands (covered by audit-log spec)
- Bot command menus (telegram setMyCommands etc.)
