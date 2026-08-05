---
status: shipped
shipped: 2026-05-01
---

# Unified message routing

The addressing and single-writer decisions the router rests on. The
router itself is [`E-routd.md`](E-routd.md).

## Decisions

**One message table, one decision point.** Every message — user input and
agent output alike — flows through `messages`; nothing enqueues or calls
back around the router. The pre-v0.25 shortcuts (`EnqueueTask`,
`OutboundEntry`) were removed and MCP tools write messages directly.
Delegation and escalation are messages carrying `forwarded_from`, not a
separate transport.

**Uniform addressing: channels are `platform:account/id`, groups are a
bare folder path.** The presence of `:` is the discriminator — a JID
without one addresses a registered group directly and the route table
does not apply (`routd/loop.go:680` `directFolder`). The `local:` prefix
was dropped for this: a prefix that only ever meant "not a platform" is
noise, and its absence carries the same information. Agent-authored rows
(sender without `:`) therefore never get content-based routing.

**Outbound is poll-reconciled, not fire-and-forget.**
`messages.status` (migration 0039) is `sent` | `pending` | `failed`.
routd writes the bot row `pending`, attempts delivery inline, marks
`sent` on success; a sweep re-dispatches `pending` rows older than 30s
and fails them after 24h (`routd/loop.go:359`). The in-memory `chanreg`
outbox stays, but only to drain on adapter reconnect — the DB row is the
durable record.

**`send` and `send_file` stay separate tools** (different
intents, different descriptions, per the tool-naming rule) but funnel
through one internal `internalSend` (`ipc/ipc.go:668`) behind the MCP
wall, so persistence and routing cannot diverge. They did diverge:
`send_file` was not recording outbound rows at all until this
consolidation. One renderer, many sinks.
