# gateway

Main poll loop, routing, command dispatch, autocalls. Imported by `gated`.

## Purpose

The heart of the router. `Gateway.Run` polls `messages` since
`lastTimestamp`, resolves each row to a group, dispatches gateway-level
commands, applies the route-target mode (trigger vs `#observe`), and
either steers a running container or enqueues a new container run via
`queue.GroupQueue`.

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

| Block                                     | Source                         | Carries                                                                                                                                                                                                                      |
| ----------------------------------------- | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<autocalls>`                             | `gateway/autocalls.go:53`      | zero-arg facts: `now`, `instance`, `folder`, `tier`, `session`                                                                                                                                                               |
| `<persona name=…>`                        | `gateway/persona.go:55`        | `PERSONA.md` frontmatter `summary:` re-anchor                                                                                                                                                                                |
| `<previous_session/>`                     | `gateway/gateway.go:2279`      | last session id/timing on a fresh session                                                                                                                                                                                    |
| `<knowledge layer=…>`                     | `diary/diary.go:36`            | recent diary entries with age labels (today/yesterday/N ago)                                                                                                                                                                 |
| `<messages>` + `<reply-to>` + `<message>` | `router/router.go:63`/`80`     | inbound batch; `<reply-to>` sibling header above the `<message>`                                                                                                                                                             |
| `<attachment …/>`                         | `gateway/gateway.go:1769,1772` | inbound media path + optional `transcript=`                                                                                                                                                                                  |
| `<observed>`                              | `gateway/gateway.go`           | trailing window of `is_observed=1` rows routed to this folder; capped by `OBSERVE_WINDOW_MESSAGES`/`OBSERVE_WINDOW_CHARS` (per-route overrides on `routes.observe_window_*`); per-topic cursor in `sessions.observed_cursor` |
| `<topic name=…/>`                         | `gateway/gateway.go:1044`      | scope envelope on every turn so the agent knows which topic it's in; empty name = main (spec 6/F rev6)                                                                                                                       |
| `<surface>slack-pane</surface>`           | `gateway/gateway.go:1076`      | emitted when trigger arrives via an open Slack assistant pane (spec 6/D)                                                                                                                                                     |
| `<pane-context jid=…/>`                   | `gateway/gateway.go:1078`      | workspace channel the user is viewing while the pane is open (spec 6/D)                                                                                                                                                      |

Coming per specs (same convention, not yet wired):

- `<proactive_reason check=…>` — `specs/5/33-proactive-interjection.md`
- `<budget_notice level=…>` — `specs/10/19-cost-caps.md:77`

When adding a new block, mirror the convention: new tag name (not
`<meta>`), ephemeral, write the rendering in exactly one place. The
"one renderer, many sinks" rule applies — the second site that emits
the same tag will drift silently. Spec docs introducing new tags
should cross-reference this table.

## Mute mode

Outbound directed at a muted target is recorded to the `messages` table
as if delivered, but `channel.Send` is never called. Used for dry-run /
shadow-routing setups where the agent should keep producing turns
without spamming the platform. The agent is **not told** it was muted —
`hadOutput` flips, `submit_turn` returns success, and the row lands with
`BotMsg=1, FromMe=1, Status=sent, RoutedTo=<chat_jid>`. Inbound flows
through untouched; mute is outbound-only.

Two env vars (CSV, case-insensitive), wired in `core/config.go:174-175`,
enforced in `gateway/gateway.go:1461` (`canSendToGroup`) and
`gateway/gateway.go:1456` (`canSendToJID`):

- `SEND_DISABLED_GROUPS` — folder names. Matches the group folder of
  the outbound row, regardless of which platform it would have hit.
  Example: `SEND_DISABLED_GROUPS=atlas,research`.
- `SEND_DISABLED_CHANNELS` — JID platform prefixes (the part before
  `:`). Mutes outbound for the entire platform across all groups.
  Example: `SEND_DISABLED_CHANNELS=discord,telegram`.

Mute is all-or-nothing per group (no per-thread, per-topic, per-verb
carve-out). Inspect the recorded outbound via `/dash/activity/` (rows
where `sender` is the folder name) or the `inspect_messages` MCP tool.
Tests assert the invariant: `gateway_test.go:1052+`
(`TestMakeOutputCallback_MutedGroup`).

## Route-target modes

A route's `target` is `<folder>[#<mode>]`. With no fragment the match
fires a turn (trigger mode). `#observe` stores the message under the
folder without firing — the agent reads observed messages via the
next trigger turn's `<observed>` block plus `inspect_messages` /
`get_history`. Verb filtering uses the existing `match` column
(`verb=mention`, …) layered by `seq` priority. See `ROUTING.md` for
the table and the Discord guild mention-only example.

## Public API

- `New(cfg *core.Config, s *store.Store) *Gateway`
- `(*Gateway).Run(ctx) error` — blocking poll loop
- `(*Gateway).Shutdown()` — flush and wait
- `(*Gateway).AddChannel(c core.Channel)`, `RemoveChannel(name)`
- `AutocallCtx` — context passed to autocall evaluators (`autocalls.go`)
- `NewLocalChannel(s)` — in-process channel for bare folder-path JIDs (group-to-group)

## Dependencies

- `core`, `store`, `router`, `queue`, `container`, `ipc`, `diary`, `groupfolder`, `grants`, `chanreg`

## Files

- `gateway.go` — poll loop, resolveGroup, handleCommand, container run
- `autocalls.go` — `<autocalls>` block rendering
- `commands.go` — gateway command dispatch (e.g. `/sticky`, `/reset`)
- `handleStickyCommand` (`gateway.go`) — bare `@<folder>` / `#<topic>` /
  bare `@` / bare `#` only. Known folder → set sticky + confirm.
  Unknown folder → fall through to the agent unchanged (no error
  reply). `@` collides with too many other meanings (`@everyone`,
  cross-instance refs, prose in non-English) to safely consume on a
  miss.
- `spawn.go` — child group spawn helpers
- `local_channel.go` — in-process channel for bare folder-path JIDs (group-to-group delegation/escalation)

## Related docs

- `ARCHITECTURE.md` (Message Flow)
- `ROUTING.md`
- `specs/5/31-autocalls.md`
