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
`messages.status` is `sent` | `pending` | `failed`. routd writes the bot
row `pending`, attempts delivery inline, and marks `sent` on success. A
sweep re-dispatches `pending` rows older than 30s
(`routd/loop.go` `maybeRetryOutbound`). `chanreg` holds no queue of its
own — the DB row is the only outbound record, and the sweep is its only
retry owner.

**The retries are bounded, and `failed` is the dead-letter state.**
`failed` is terminal: the row keeps its content, `PendingOutbound`
excludes it, and no sweep sends it again. Two rules end the retries:

- A **permanent** refusal (`chanlib.ErrPermanent`, a 4xx from the
  adapter) dead-letters the row on the first attempt and spends no
  attempt budget. A retry cannot repair a deleted chat or a malformed
  request.
- A **transient** failure spends one attempt
  (`messages.delivery_attempts`, migration 0034). The row dead-letters
  when the attempts reach `MAX_OUTBOUND_RETRY` (default 10, about 5
  minutes at the 30s cadence).

Age is not a bound. The retired 24h rule failed a row that the sweep had
never attempted — a routd outage longer than 24h discarded the whole
backlog unsent — so the attempt count is the only bound. Nothing
disappears: every outbound message ends as `sent` or as an inspectable
`failed` row. `delivery_attempts` tells the two dead-letter causes
apart: 0 means the platform refused it, the cap means the retries ran
out. dashd's routd page counts both `pending outbound` and
`dead-lettered`.

**A failed send writes its row too.** The MCP `send`/`reply`/`send_file`
tools return the delivery error to the agent AND record the undelivered
row (`ipc/ipc.go` `recordOutboundFailure`). A transient failure lands
`pending`, so the sweep owns the retry; a permanent refusal lands
`failed`. A failed `send_file` always dead-letters, because the
text-only sweep would repeat it as a captionless, fileless message —
the REST document twin (`routd/turns.go` `handleDocument`) does the
same. Before this, a failed send wrote nothing and the message vanished.

**`send` and `send_file` stay separate tools** (different
intents, different descriptions, per the tool-naming rule) but funnel
through one internal `internalSend` (`ipc/ipc.go`) behind the MCP
wall, so persistence and routing cannot diverge. They did diverge:
`send_file` was not recording outbound rows at all until this
consolidation. One renderer, many sinks.
