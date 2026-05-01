# gateway

Main poll loop, routing, command dispatch, autocalls. Imported by `gated`.

## Purpose

The heart of the router. `Gateway.Run` polls `messages` since
`lastTimestamp`, resolves each row to a group, dispatches gateway-level
commands, applies the impulse gate, and either steers a running container
or enqueues a new container run via `queue.GroupQueue`.

## Autocalls

The agent prompt opens with an inline `<autocalls>` block produced by
`gateway/autocalls.go`. Each registered autocall is a zero-arg
read-only fact (current time, instance, folder, tier, session id, …)
evaluated at prompt-build time. This replaces the per-turn `<clock/>`
MCP roundtrip — the deleted `router.ClockXml` shape — and keeps
zero-arg facts to one prompt line each. Autocalls returning empty
strings are skipped. See `specs/5/31-autocalls.md` and EXTENDING for
adding one.

## Mute mode

Outbound directed at a group in `SEND_DISABLED_CHANNELS` (or its
`SEND_DISABLED_GROUPS` companion) is recorded to the messages table as
if delivered, but `channel.Send` is never called. Used for dry-run /
shadow-routing setups where the agent should keep producing turns
without spamming the platform. Tests assert the invariant
(`gateway_test.go::TestMakeOutputCallback_MutedGroup`).

## Public API

- `New(cfg *core.Config, s *store.Store) *Gateway`
- `(*Gateway).Run(ctx) error` — blocking poll loop
- `(*Gateway).Shutdown()` — flush and wait
- `(*Gateway).AddChannel(c core.Channel)`, `RemoveChannel(name)`
- `AutocallCtx` — context passed to autocall evaluators (`autocalls.go`)
- `ImpulseCfg`, `ParseImpulseCfg(raw)` — per-route impulse gate config
- `NewLocalChannel(s)` — in-process channel for bare folder-path JIDs (group-to-group)

## Dependencies

- `core`, `store`, `router`, `queue`, `container`, `ipc`, `diary`, `groupfolder`, `grants`, `chanreg`

## Files

- `gateway.go` — poll loop, resolveGroup, handleCommand, container run
- `autocalls.go` — `<autocalls>` block rendering
- `commands.go` — gateway command dispatch (e.g. `/sticky`, `/reset`)
- `impulse.go` — weight-based batching gate
- `spawn.go` — child group spawn helpers
- `local_channel.go` — in-process channel for bare folder-path JIDs (group-to-group)

## Related docs

- `ARCHITECTURE.md` (Message Flow)
- `ROUTING.md`
- `specs/5/31-autocalls.md`
