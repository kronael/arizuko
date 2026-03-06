# Message WAL (Write-Ahead Log)

Reliable message delivery with cursor rollback for piped messages.

## Problem

Two message delivery paths exist:

1. **New container** — `processGroupMessages()` advances cursor,
   spawns container, rolls back on error. Reliable.
2. **Piping to active container** — `startMessageLoop()` writes
   IPC file to `input/`, advances cursor immediately. No rollback.

In path 2, if the container crashes between IPC file write and
file read, messages are lost. Cursor already advanced past them.
User gets "Something went wrong" but the piped messages are gone.

## Why it's not fixed in v1

The piping path and container exit handler live in different
functions (`startMessageLoop` vs `processGroupMessages`). Threading
rollback state between them requires shared mutable state and
introduces its own race conditions.

Not advancing the cursor is worse — every piped message would
be re-fetched on the next 2s poll, causing guaranteed duplicates
on every pipe, not just on crash.

Current tradeoff: rare message loss (microsecond crash window)
with user retry prompt vs frequent guaranteed duplicates. The
v1 choice is correct for v1.

## Solution: WAL

Instead of cursor-based delivery, use a write-ahead log:

1. **On pipe**: write message IDs to `pending_delivery` table
   (WAL entry) before writing IPC file. Don't advance cursor.
2. **On agent ack**: agent reads IPC file, sends ack back via
   IPC reply. Gateway deletes WAL entry and advances cursor.
3. **On container exit without ack**: WAL entries survive.
   Next container spawn replays unacked messages.
4. **On duplicate detection**: agent-side dedup via message ID
   (already present in formatted messages).

### Schema

```sql
CREATE TABLE pending_delivery (
  id INTEGER PRIMARY KEY,
  group_folder TEXT NOT NULL,
  message_id TEXT NOT NULL,
  written_at TEXT NOT NULL,
  acked_at TEXT
);
```

### Flow

```
user message → store in DB → write WAL entry → write IPC file
                                                     ↓
                                              agent reads file
                                                     ↓
                                              agent sends ack
                                                     ↓
                                          gateway marks WAL acked
                                                     ↓
                                          advance cursor past acked
```

### Cursor becomes derived

With WAL, the cursor (`lastAgentTimestamp`) becomes derived from
the WAL state: it's the timestamp of the most recent acked message.
No more direct cursor manipulation in two places.

### Dedup

Agent deduplicates by message ID. If the same message is delivered
twice (WAL replay after crash), agent sees the same ID and skips.
Message IDs are already included in formatted output but not
currently parsed by the agent — would need a thin protocol addition.

## Scope

This is a v2 feature. v1's cursor-advance approach is adequate
for single-group, low-volume usage. WAL matters when:

- Multi-message piping is frequent (active conversations)
- Container crashes are non-rare (resource limits, timeouts)
- Message loss is unacceptable (transactional workflows)

## Dependencies

- Message ID propagation to agent (already in DB, needs IPC format)
- Agent-side ack protocol (new IPC message type)
- WAL table + cleanup (trivial schema addition)
