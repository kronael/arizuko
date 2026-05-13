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

## Per-turn ephemeral XML blocks

Every inbound turn the gateway builds an envelope of small XML-shaped
blocks prepended (or attached) to the agent's prompt. They share three
properties: **XML-shaped**, **never persisted to `messages`**,
**per-turn scope only** (recomputed from scratch next turn). Other
systems (e.g. muaddib's `<meta>...</meta>`) unify these under one tag;
arizuko keeps them as distinct named tags so the agent can pattern-match
on intent.

| Block                                     | Source                         | Carries                                                          |
| ----------------------------------------- | ------------------------------ | ---------------------------------------------------------------- |
| `<autocalls>`                             | `gateway/autocalls.go:53`      | zero-arg facts: `now`, `instance`, `folder`, `tier`, `session`   |
| `<persona name=…>`                        | `gateway/persona.go:55`        | `PERSONA.md` frontmatter `summary:` re-anchor                    |
| `<previous_session/>`                     | `gateway/gateway.go:1799`      | last session id/timing on a fresh session                        |
| `<knowledge layer=…>`                     | `diary/diary.go:36`            | recent diary entries with age labels (today/yesterday/N ago)     |
| `<messages>` + `<reply-to>` + `<message>` | `router/router.go:63`/`80`     | inbound batch; `<reply-to>` sibling header above the `<message>` |
| `<attachment …/>`                         | `gateway/gateway.go:1350,1353` | inbound media path + optional `transcript=`                      |

Coming per specs (same convention, not yet wired):

- `<proactive_reason validator=… score=…>` — `specs/5/33-proactive-interjection.md:78`
- `<budget_notice level=…>` — `specs/5/34-cost-caps.md:77`

When adding a new block, mirror the convention: new tag name (not
`<meta>`), ephemeral, write the rendering in exactly one place. The
"one renderer, many sinks" rule applies — the second site that emits
the same tag will drift silently. Spec docs introducing new tags
should cross-reference this table.

## Mute mode

Outbound directed at a group in `SEND_DISABLED_CHANNELS` (or its
`SEND_DISABLED_GROUPS` companion) is recorded to the messages table as
if delivered, but `channel.Send` is never called. Used for dry-run /
shadow-routing setups where the agent should keep producing turns
without spamming the platform. Tests assert the invariant
(`gateway_test.go::TestMakeOutputCallback_MutedGroup`).

## Impulse gate

`impulse.go` batches messages per JID and fires when accumulated weight reaches
the threshold (default 100). Each message contributes `weightFor(cfg, verb)` —
default 100 for any verb not listed in the route's `ImpulseConfig.Weights`.

Setting `{"weights":{"message":0}}` on a route makes non-mention guild messages
accumulate as context without firing. `mention` (not overridden) retains the
default weight of 100 and fires immediately. This is the automatic default for
Discord guild channels registered via `group add`.

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
- `local_channel.go` — in-process channel for bare folder-path JIDs (group-to-group delegation/escalation)

## Related docs

- `ARCHITECTURE.md` (Message Flow)
- `ROUTING.md`
- `specs/5/31-autocalls.md`
