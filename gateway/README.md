# gateway

Main poll loop, routing, command dispatch, autocalls. Imported by `gated`.

## Purpose

The heart of the router. `Gateway.Run` polls `messages` since
`lastTimestamp`, resolves each row to a group, dispatches gateway-level
commands, applies the impulse gate, and either steers a running container
or enqueues a new container run via `queue.GroupQueue`.

## Public API

- `New(cfg *core.Config, s *store.Store) *Gateway`
- `(*Gateway).Run(ctx) error` — blocking poll loop
- `(*Gateway).Shutdown()` — flush and wait
- `(*Gateway).AddChannel(c core.Channel)`, `RemoveChannel(name)`
- `AutocallCtx` — context passed to autocall evaluators (`autocalls.go`)
- `ImpulseCfg`, `ParseImpulseCfg(raw)` — per-route impulse gate config
- `NewLocalChannel(s)` — in-process channel for `local:` JIDs

## Dependencies

- `core`, `store`, `router`, `queue`, `container`, `ipc`, `diary`, `groupfolder`, `grants`, `chanreg`

## Files

- `gateway.go` — poll loop, resolveGroup, handleCommand, container run
- `autocalls.go` — `<autocalls>` block rendering
- `commands.go` — gateway command dispatch (e.g. `/sticky`, `/reset`)
- `impulse.go` — weight-based batching gate
- `spawn.go` — child group spawn helpers
- `local_channel.go` — `local:` CLI channel

## Related docs

- `ARCHITECTURE.md` (Message Flow)
- `ROUTING.md`
- `specs/7/34-autocalls.md`
