---
status: spec
depends: [B-route-mode-ingestion]
relates-to: [4/23-topic-routing, 3/Y-thread-routing, 3/a-sticky-routing]
revision: 2
---

# specs/6/F — topic lineage: forks + per-topic observed cursor

## v1 scope (this spec)

Four things ship together:

1. **`fork_topic(parent, child)` MCP command** — explicit topic
   forking with parent context inherited up to the fork point.
2. **Per-topic `observed_cursor`** — fixes the "topic A consumes
   observed window, topic B never sees them" problem.
3. **Default-fork-from-main** — every non-main topic auto-forks from
   main on first session creation. Slack `#deploy` started by user
   command, web-topic created via slink, Telegram bot menu topic —
   all start with main's recent history as `<inherited>` context.
4. **Thread auto-fork (implicit)** — every adapter that creates a
   new Topic for a platform thread (Slack thread_ts, Discord thread
   channel, Telegram thread id) inherits via #3 automatically. No
   adapter-side wiring needed; thread-spawned topics ARE non-main
   topics and EnsureTopicLineage forks them like any other.

Oracle's earlier objection to bundling #3 and #4 was framed as
"silent behavior change on existing folders" and "Slack threads
aren't forks." Operator's call: ship clean over back-compat-cautious.
The objections are real but the operator wants the simpler shape;
opt-out via `fork_topic --force` reassignment if a topic needs
an empty-start.

## Pre-requisite: extract `buildAgentPrompt`

**Before any lineage code lands**, the prompt-rendering call sites
in `gateway.go:728` (chat path) and `:798` (web-topic path) must
collapse to one function. Today they hand-assemble:

```go
prompt := sysMsgs + autocallsBlock + personaBlock + observedRule + FormatMessages(...)
```

…with subtle differences (web path skips observed). Adding lineage
to one path silently makes the other drift further. Same shape as
the cold-start vs steered drift documented in CLAUDE.md.

The extraction is its own commit, no behavior change. The signature:

```go
func (g *Gateway) buildAgentPrompt(
    group  core.Group,
    chatJid string,
    topic   string,
    trigger []core.Message,
) string
```

Both existing call sites call it. After this commit the codebase has
one renderer. Lineage code then has one place to land.

## The primitive

Three nullable columns on `sessions`:

```sql
ALTER TABLE sessions ADD COLUMN parent_topic     TEXT;     -- which topic forked from; NULL = no parent
ALTER TABLE sessions ADD COLUMN forked_at        TEXT;     -- RFC3339Nano UTC; the fork point
ALTER TABLE sessions ADD COLUMN observed_cursor  TEXT;     -- RFC3339Nano UTC; last observed ts processed
```

**Format invariant**: every timestamp in arizuko's schema is
`time.Now().UTC().Format(time.RFC3339Nano)`. The store helpers
`messages.go:25` already do this for `messages.timestamp`; lineage
columns join the same convention. **No `strftime` in migrations** —
backfills compute the value in Go before insert, so format stays
single-sourced.

## Reading a topic's full context

```
1. trigger_msgs  = MessagesSince(chat_jid, agent_cursor)               // unchanged
2. parent_msgs   = TopicHistoryThrough(folder, parent_topic, forked_at, INHERIT_WINDOW_*)
                   // skipped when parent_topic IS NULL
3. observed_msgs = ObservedSince(folder, observed_cursor, OBSERVE_WINDOW_*)
                   // separate from inherited; separate cap
4. render: buildAgentPrompt → <inherited>…</inherited> <observed>…</observed> + trigger
```

Rendered into the existing prompt. Two new env vars:

- `INHERIT_WINDOW_MESSAGES` (default 50)
- `INHERIT_WINDOW_CHARS` (default 20000)

Separate from `OBSERVE_WINDOW_*` because they bound semantically
different things — inherited is "context up to here", observed is
ambient sidechannel. Conflating them would be the same overload-one-
knob-with-two-meanings smell that route mode just escaped.

## Cursor advancement — at-least-once semantics

Cursor advance is **not transactional with the agent turn**. The
spec accepts at-least-once delivery of `<observed>` messages on
crash recovery. Rationale:

- The agent's prompt already includes the rule "observed messages
  are context, not requests" (from spec 6/B). Re-feeding the same
  observed message on a retry is benign — the agent doesn't act on
  it twice.
- The alternative (advance-before-run + lose on failure) silently
  drops messages, which IS a correctness bug.
- The third alternative (advance inside `EndSession`, atomic with
  turn success record) is real engineering but spec-out-of-scope
  for v1; can ship later without schema change.

Cursor advance lives in `buildAgentPrompt` right after reading
`ObservedSince` — before the agent runs. Same place, same write.

Documented in `EXTENDING.md`: "Observed messages may surface twice
on crash recovery. The agent's prompt rule prevents double-response."

## Migration backfill — neither now() nor zero

```sql
-- 0055-topic-lineage.sql
ALTER TABLE sessions ADD COLUMN parent_topic    TEXT;
ALTER TABLE sessions ADD COLUMN forked_at       TEXT;
ALTER TABLE sessions ADD COLUMN observed_cursor TEXT;

-- No backfill in SQL. observed_cursor stays NULL; Go layer
-- treats NULL as "see all current observed messages up to
-- the standard window cap" on first read after migration.
-- This is the same behavior as a fresh topic and matches
-- the spec 6/B promise.

CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);
```

(Index without `WHERE` predicate per oracle: partial index excluding
~1 root row per folder isn't worth its weight.)

`ObservedSince(folder, cursor, ...)` treats `cursor IS NULL` as
"no lower bound; let OBSERVE*WINDOW*\* cap it." This matches today's
`ObservedTail` behavior exactly for existing rows — zero behavior
change at migration. First turn after migration writes the cursor.

## `fork_topic(parent, child)` MCP tool

```
fork_topic(parent: string, child: string, force: bool = false)
```

- Errors if child already exists with `topic_exists` unless `force=true`.
- Inserts `sessions(group_folder=current, topic=child,
parent_topic=parent, forked_at=now(), observed_cursor=now(),
session_id=NEW_UUID)`.
- Does **not** touch `chat_reply_state` — reply-threading is per-JID
  and orthogonal to topic lineage. Documented in the spec, REST/MCP
  docs, and `ROUTING.md`. (Operator who wants reply-thread reset
  uses the separate `clear_reply_state` tool.)
- Returns `{topic: child, parent_topic: parent, forked_at: ts}`.

REST counterpart per the "MCP+REST hand-rolled and uniform" CLAUDE.md
principle: `POST /v1/topics/{folder}/fork {parent, child}`. Same
handler shape as other resreg-style endpoints.

## What this is NOT (v1)

- **NOT crash-safe-atomic cursor.** At-least-once with agent-prompt
  rule preventing double-action.
- **NOT replay of observed at fork.** Child's `observed_cursor =
now()` — child sees observed messages that arrive AFTER fork, not
  before. The "before" lives in `<inherited>` from parent.
- **NOT a fork from arbitrary message ID.** Fork is always from
  parent's state at `now()`. "Fork from message X" is a different
  primitive; spec defers.
- **NOT REST yet.** `fork_topic` ships MCP-only. The `POST
/v1/topics/{folder}/fork` REST counterpart waits on the resreg
  pattern for non-CRUD verbs.
- **NOT per-folder opt-out for default-fork-from-main.** Operator
  asked for the simple shape; every non-main topic forks from main.
  If a topic needs an empty session, the operator can
  `fork_topic main #x --force` (force-resetting from main is still
  default-fork; to truly start empty, delete the row and let it
  be re-created — but the next create will fork again unless we
  add an opt-out, which v1 deliberately doesn't).

## Code changes (revised)

| File                                      | Change                                                                                                                | LOC              |
| ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------- | ---------------- |
| `gateway/gateway.go`                      | extract `buildAgentPrompt` (Phase 0)                                                                                  | 0 net (refactor) |
| `store/migrations/0055-topic-lineage.sql` | new                                                                                                                   | 8                |
| `store/sessions.go`                       | add `TopicLineage()`, `UpdateObservedCursor()`, `Fork()`                                                              | ~60              |
| `store/messages.go`                       | rename `ObservedTail` → `ObservedSince(folder, cursor, ...)`; add `TopicHistoryThrough(folder, topic, beforeTS, ...)` | ~40              |
| `gateway/gateway.go`                      | wire lineage into `buildAgentPrompt`; advance cursor                                                                  | ~25              |
| `core/types.go`                           | add `core.TopicLineage` struct                                                                                        | ~8               |
| `ipc/ipc.go`                              | new MCP tool `fork_topic`                                                                                             | ~30              |
| `proxyd/...` or `dashd/...`               | REST `POST /v1/topics/{folder}/fork`                                                                                  | ~30              |
| Tests                                     | new + regression                                                                                                      | ~120             |

**Net: ~320 LOC** (vs ~420 in the original draft).

## Migration order

1. **Phase 0** — extract `buildAgentPrompt`. Zero behavior change.
   Commit `[refactor] gateway: one renderer for chat + web-topic`.
2. **Phase 1** — schema migration + `ObservedSince(folder, cursor)` +
   gateway reads/advances `observed_cursor`. Per-topic cursor active.
   Ship. Smoke test: open two topics in one folder, send observed
   message between them, both topics should see it on next turn.
3. **Phase 2** — `TopicHistoryThrough` + `<inherited>` block rendering
   when `parent_topic` is set. No way to set parent_topic yet — code
   path inactive but tested.
4. **Phase 3** — `fork_topic` MCP + REST. Now operators can mint
   forks; `<inherited>` block lights up for forked children.
5. **Smoke** on sloth (lower-stakes than krons): operator runs
   `fork_topic main #deploy`, verifies child sees main's tail as
   inherited. Then krons.

Each phase is independently revertable.

## Risks (revised after oracle)

- **`<inherited>` cap is the right size?** Operator-tunable via
  `INHERIT_WINDOW_*`; default 50 msgs / 20KB based on typical main
  topic size. If too small, fork loses context; if too big, blows
  prompt budget. Watch in smoke; tune.
- **At-least-once observed**: documented in EXTENDING.md and ant
  CLAUDE.md. Future work: atomic-advance variant after `EndSession`
  shape is finalized.
- **`fork_topic --force` on a topic with active container**: container
  keeps its session_id until next spawn. Fork swaps session_id in DB;
  next spawn picks up new lineage. Document this lag.

## Out of scope (tracked as follow-ups)

- v2: per-folder `default_fork_main` setting + opt-in default.
- v2: `fork_topic` from arbitrary timestamp / message id.
- v2: native thread auto-fork (slack/discord/telegram-platform-native).
- v2: GC of orphaned topics with no recent messages.
- v2: atomic observed-cursor advance via `EndSession` integration.

## Open questions

- Resolved: `parent_topic` NULL vs `""` for root — NULL (no parent).
  Empty string is reserved for the main topic name.
- Resolved: timestamp format — RFC3339Nano UTC everywhere, computed
  in Go, never via SQLite `strftime`.
- Resolved: cursor advance semantics — at-least-once + agent-prompt
  rule.
- Open: what to do if `parent_topic` references a non-existent sessions
  row? Treat as no inheritance (empty `<inherited>` block), don't
  error. Strict-not-magical says: error. Spec says: tolerate; the
  parent might have been GC'd later. Pick on first hit.
