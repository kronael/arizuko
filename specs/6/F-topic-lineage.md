---
status: spec
depends: [B-route-mode-ingestion]
relates-to: [4/23-topic-routing, 3/Y-thread-routing, 3/a-sticky-routing]
---

# specs/6/F — topic lineage: forks, default-fork-from-main, per-topic observed

## What this solves

Four operator asks, one primitive:

1. **`fork` command** — branch a topic from another's current state.
2. **Platform threads start by forking** — when Slack/Discord/Telegram
   spawns a new thread off a parent message, the child topic inherits
   parent context up to the fork point. Today the child starts empty.
3. **New topics default to forking `main`** — `/new #deploy` should
   inherit main's context up to creation time, not start from scratch.
4. **All topics in a group see observed messages** — currently
   `is_observed=1` messages are read once per folder; subsequent topic
   turns either re-feed (double-vision) or never see them. Each topic
   needs its own observed cursor.

The shape: **a topic carries lineage** — `parent_topic`, `forked_at`,
`observed_cursor`. With those three columns, all four asks fall out
without further primitives.

## The primitive

A topic is a `(folder, topic)` pair in `sessions`. After this spec it
gains three nullable columns:

```sql
ALTER TABLE sessions ADD COLUMN parent_topic     TEXT;     -- which topic forked from; "" = main
ALTER TABLE sessions ADD COLUMN forked_at        TEXT;     -- ISO8601 timestamp the fork point was taken
ALTER TABLE sessions ADD COLUMN observed_cursor  TEXT;     -- ISO8601 timestamp of last observed message processed
```

Three nullable columns on an existing table. No new table. No new
relations. Index `(folder, parent_topic)` for fork-children lookup.

### Defaults

- New row from `GetOrCreateSession(folder, topic)`:
  - `parent_topic = ""` (main)
  - `forked_at    = now()`
  - `observed_cursor = now()`
- The `main` topic (topic = `""`) has `parent_topic = NULL` (root, no parent).

### Reading a topic's full context

On every trigger turn for `(folder, topic)`:

```
1. trigger_msgs   = MessagesSince(chat_jid, agent_cursor)          // unchanged
2. parent_msgs    = TopicHistoryThrough(folder, parent_topic, forked_at)
                    // messages routed to parent_topic, timestamp <= forked_at
                    // empty if parent_topic = "" or NULL
3. observed_msgs  = ObservedSince(folder, observed_cursor)
                    // observed messages after this topic's cursor
4. advance observed_cursor = max(observed_msgs.timestamp)
5. render prompt with parent_msgs + observed_msgs + trigger_msgs
```

Concretely: the agent sees the parent's history up to the fork
moment as `<inherited>` context, the observed window as `<observed>`,
and the current trigger turn as the live thread.

## How each ask falls out

### 1. `fork` command (MCP tool)

```
fork_topic(parent: string, child: string)
  -> upsert sessions row (folder, child)
       with parent_topic = parent, forked_at = now(), observed_cursor = now()
  -> reset session_id (new fresh agent state); child agent starts
     with parent's history as `<inherited>` block
```

Exposed via MCP for the agent itself, and via REST/dashd for operators.
~30 LOC.

### 2. Platform threads = forks

Each adapter already maps native thread IDs to `Message.Topic`. The
change: when the inbound carries a parent message (Slack `parent_user_message.ts`,
Discord `MessageReference.MessageID`, Telegram `reply_to_message`), the
adapter populates a new `Message.ParentTopic` field with the parent's
topic. The gateway, when calling `GetOrCreateSession`, passes the
parent if present.

```go
// In each adapter's inbound mapping:
if reply := m.ReplyToMessage; reply != nil {
    inbound.ParentTopic = parentTopicFor(reply)  // adapter-specific
}
```

`parentTopicFor` is one line per adapter: pick the parent message's
own topic if known, else `""` (main). ~10 LOC per adapter × 4 = ~40 LOC.

### 3. New topics fork main by default

This is just the defaults at top: `parent_topic = ""` (main) when
inserting a new sessions row. Behavior is automatic for every
non-thread topic command (`/new #deploy`, sticky `#topic`, etc.).
Zero extra code if the defaults are set correctly in
`GetOrCreateSession`.

### 4. Per-topic observed cursor

`ObservedTail(folder, maxMsgs, maxChars)` becomes
`ObservedSince(folder, observed_cursor, maxMsgs, maxChars)`. Reads
messages where `routed_to=folder AND is_observed=1 AND timestamp >
observed_cursor`. Gateway advances `observed_cursor` to the youngest
observed message timestamp after rendering the prompt. Each topic
keeps its own cursor in the `sessions` row.

Edge case: if a topic is created today (cursor = now()), it sees only
observed messages that arrive AFTER creation. Older observed messages
arrive only via the parent's inherited history (step 2). That's the
correct boundary — observed context is forward-looking from the
topic's creation, not retrospective.

## Schema migration

`store/migrations/0055-topic-lineage.sql`:

```sql
-- Three columns on sessions. All nullable; existing rows continue to work
-- (main session, topic=""). Defaults applied at insert site in Go.
ALTER TABLE sessions ADD COLUMN parent_topic     TEXT;
ALTER TABLE sessions ADD COLUMN forked_at        TEXT;
ALTER TABLE sessions ADD COLUMN observed_cursor  TEXT;

CREATE INDEX idx_sessions_parent ON sessions(group_folder, parent_topic)
  WHERE parent_topic IS NOT NULL;

-- Existing rows: backfill observed_cursor=now() so the first turn after
-- migration doesn't replay the full observed history.
UPDATE sessions
SET observed_cursor = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE observed_cursor IS NULL;
```

One migration. Backwards-compatible: rows without lineage just use
defaults (parent = main, forked_at = creation, observed = creation).

## Code changes

| File                                                                  | Change                                                                                                                | LOC               |
| --------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- | ----------------- |
| `store/migrations/0055-topic-lineage.sql`                             | new                                                                                                                   | 12                |
| `store/sessions.go`                                                   | extend `GetOrCreateSession`, add `TopicLineage`, `AdvanceObservedCursor`, `ForkTopic`                                 | ~80               |
| `store/messages.go`                                                   | rename `ObservedTail` → `ObservedSince(folder, cursor, ...)`; add `TopicHistoryThrough(folder, topic, beforeTS, ...)` | ~40               |
| `gateway/gateway.go`                                                  | call new functions; thread `parent_topic` from inbound; advance cursor after rendering                                | ~30               |
| `core/types.go`                                                       | add `Message.ParentTopic string` field; `core.TopicLineage` struct                                                    | ~10               |
| `chanlib/types.go` + per-adapter (`slakd`, `discd`, `teled`, `whapd`) | map native parent-message to `ParentTopic`                                                                            | ~10 each × 4 = 40 |
| `ipc/ipc.go`                                                          | new MCP tool `fork_topic`                                                                                             | ~30               |
| `proxyd/...` or `dashd/...`                                           | REST endpoint for fork (per "MCP+REST uniform" principle)                                                             | ~30               |
| Tests (per call site + integration)                                   | new                                                                                                                   | ~150              |

**Net: ~420 LOC** including tests. Three concerns kept orthogonal:
(a) lineage in store, (b) inbound parent detection in adapters,
(c) cursor advancement in gateway. No daemon learns more than one new
verb.

## What this is NOT

- **NOT a per-message branching system.** Fork takes a snapshot of
  topic state at `now()`; it doesn't let you "fork from message ID X
  in the middle." If we ever need that, `forked_at` accepts any
  timestamp — but the command surface ships with `now()` only.
- **NOT a session-state copy.** Forked child starts with a fresh
  `session_id`. The parent's _message history_ is what's inherited via
  the `<inherited>` block; the parent's _agent memory/turn state_ is
  not copied. Clean separation.
- **NOT per-chat_jid topic scoping.** Topics remain `(folder, topic)`,
  shared across all chat_jids that route to the folder. Two channels
  both posting to `#deploy` in the same folder participate in the same
  topic.
- **NOT a replacement for `agent_cursor`.** The chat-level cursor on
  `chats(jid)` stays — it's the per-channel watermark for "what
  messages have been processed at all." The topic-level
  `observed_cursor` is a different layer — it's per-topic "which
  observed messages has THIS conversation seen."
- **NOT auto-fork of non-thread messages.** A plain channel message
  (no thread, no `#topic` command) lands in main as before. Only
  explicit threads + the `fork` command create child topics.

## Prompt rendering

The `<observed>` block stays as today (per spec 6/B). New block:
`<inherited from="parent_topic" through="2026-05-16T08:42:11Z">…</inherited>`
emitted when `parent_topic != ""` and parent has messages before
`forked_at`. The agent's prompt rule (added to ant CLAUDE.md):

> Inherited messages are the parent topic's history up to the fork
> point. Treat as background; do not re-respond to them. The live
> thread starts after this block.

Same shape as the existing `<observed>` rule.

## Migration order

1. **Ship the schema migration** (0055) alone — adds nullable columns,
   no behavior change. Verify no regressions.
2. **Per-topic `observed_cursor`** — gateway reads + advances. Other
   features still work because parent_topic / forked_at default to
   "fork from main at row creation," but `<inherited>` rendering is
   gated off (next step).
3. **Inherited rendering + parent detection in adapters** — emit the
   `<inherited>` block when parent has messages. Per-adapter PRs for
   slakd/discd/teled/whapd in order.
4. **`fork_topic` MCP + REST** — exposes the primitive operators can
   call directly. Last because it's the most user-facing change and
   wants a stable underlying primitive first.

Each step is independently revertable. Each ships its own tests.

## Risks

- **`<inherited>` block size**: parent's history can be enormous. Cap
  by the same `OBSERVE_WINDOW_*` env vars (the operator already tunes
  these); inherited block reuses the same window math.
- **Forked-cursor advancement under steered batches**: when the
  gateway steers a batch into a running container, the observed cursor
  advance must happen for each topic that fires, not just the first.
  Single call site in the rendering function — straightforward.
- **`fork_topic(parent, child)` where child already exists**: error
  by default; `--force` flag for explicit reset. Per the
  strict-not-magical principle (CLAUDE.md), no implicit overwrite.
- **Thread storms**: a channel with many threads creates many topic
  rows. `sessions` is unbounded today; no GC. Out of scope for this
  spec; tracked separately as a sessions-pruning task.

## Open questions

- **Should `forked_at` accept past timestamps via the MCP API?**
  Probably no for v1 — only `now()`. Adding "fork from message ID X"
  is a real new primitive, separate spec.
- **Should the `<inherited>` block include the parent topic's own
  observed messages, or only its trigger messages?** Probably only
  trigger messages — observed-of-observed risks recursive blow-up.
- **What happens to `observed_cursor` when topics fork from each other
  recursively?** Each topic owns its own cursor; no inheritance of
  cursors. Each fork starts at `now()`. Simple, no recursion.
- **Should we GC orphaned topics (no messages for N days)?** Yes
  eventually; out of scope here. Track as `sessions-gc` task.
