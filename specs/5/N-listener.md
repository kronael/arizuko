---
status: planned
---

# Passive Listener Groups

A group mode that ingests messages from external channels without running an
agent per message. Messages accumulate; a scheduled agent run compiles a
digest and sends it to a designated destination JID.

Use cases: monitor a Telegram group for market signals, track a Discord
server for relevant events, compile daily summaries of a channel.

---

## Problem

Every registered group today triggers an agent container per message (or per
message batch). For high-volume read-only channels this is wasteful and
produces noise: the agent has nothing meaningful to say about each individual
message.

What is needed is a way to say: collect everything from this source, run the
agent on a schedule, let the agent decide what is worth reporting.

---

## Design

### Group mode field

Add `mode` to `groups`:

```sql
ALTER TABLE groups ADD COLUMN mode TEXT NOT NULL DEFAULT 'active';
-- 'active'   — current behaviour, agent runs per message batch
-- 'listener' — no per-message agent runs; messages accumulate
```

`Group.Mode` field in `core.Group`. No other type changes required.

### Gateway change

In `pollOnce` / `processGroupMessages`, skip `EnqueueMessageCheck` for
listener groups. Messages are still stored by the channel adapter (they flow
through the normal `POST /v1/messages` → `store.PutMessage` path). The
gateway just does not dispatch them.

```go
if group.Mode == "listener" {
    // no agent run; message stored, cursor not advanced
    continue
}
```

The listener group's agent cursor (`store.GetAgentCursor`) is only advanced
by the digest run, not by incoming messages.

### Digest scheduled task

The digest is a `scheduled_tasks` row owned by the listener group, with a
cron expression (e.g. `0 8 * * *` for daily at 08:00). When `timed` fires:

1. Inserts a prompt message into `messages` with `sender=scheduler`
2. Gateway picks it up in the normal poll loop
3. `processGroupMessages` runs — but now it is a scheduled run, so the
   prompt is from `scheduler`, not from an external user
4. The agent receives all accumulated messages since `last_digest_at`
   (resolved as `MessagesSince(jid, lastDigestCursor)`) plus the prompt
5. Agent produces a digest and calls `send_message(jid=<dest_jid>)`
6. Agent cursor advances; `last_digest_cursor` updated

The prompt injected by `timed` should include the target destination so the
agent knows where to send the output. Simplest encoding: the task `chat_jid`
is the listener group JID; the prompt is the digest instruction with an
XML tag for destination:

```
<digest>
  <dest>tg:123456789</dest>
  Summarise the messages above. Focus on actionable signals.
</digest>
```

The operator writes this prompt when creating the scheduled task. No new
schema field needed — destination is in the prompt.

### Message TTL / cursor discipline

Listener groups accumulate messages but must not grow unbounded.

Two mechanisms:

1. **Digest cursor**: after each digest run, the agent cursor advances past
   the processed messages. Messages before the cursor are already processed;
   they are retained by normal store retention (same as active groups).

2. **Store-level TTL**: add a `message_ttl_days` column to `groups`
   (default NULL = no TTL). A periodic cleanup job (daily, in `timed`) deletes
   messages older than TTL for groups that have it set. This is a separate
   concern from digest cadence.

```sql
ALTER TABLE groups ADD COLUMN message_ttl_days INTEGER;
```

Cleanup query (run daily in `timed`):

```sql
DELETE FROM messages
WHERE chat_jid IN (
    SELECT jid FROM groups WHERE message_ttl_days IS NOT NULL
)
AND timestamp < datetime('now', '-' || (
    SELECT message_ttl_days FROM groups WHERE jid = messages.chat_jid
) || ' days');
```

### MCP tools — no new tools needed

The digest agent uses existing tools:

- `send_message(jid, text)` — deliver the digest to the destination
- File access to its group folder for scratch notes across runs

The prompt drives the behaviour; the tools are already there.

### Configuration

Listener group created via `arizuko group` CLI or directly in
`groups`:

```
mode          = "listener"
message_ttl_days = 7        (optional)
```

Digest task registered via `arizuko task create` or MCP `create_task`:

```
chat_jid  = <listener group JID>
cron      = "0 8 * * *"
prompt    = "<digest><dest>tg:123456789</dest>Daily market digest...</dest>"
```

---

## Schema changes

```sql
ALTER TABLE groups ADD COLUMN mode TEXT NOT NULL DEFAULT 'active';
ALTER TABLE groups ADD COLUMN message_ttl_days INTEGER;
```

No new tables. No migration to existing `scheduled_tasks`.

---

## Affected code

| Location              | Change                                                           |
| --------------------- | ---------------------------------------------------------------- |
| `core/types.go`       | Add `Mode string` to `Group`                                     |
| `store/store.go`      | Read `mode` + `message_ttl_days` in `AllGroups`, `GroupByFolder` |
| `gateway/gateway.go`  | Skip `EnqueueMessageCheck` for listener groups in `pollOnce`     |
| `timed/main.go`       | Daily cleanup of expired messages for TTL groups                 |
| `cmd/arizuko/main.go` | `group add --mode listener --ttl 7` flag                         |

---

## Open questions

**Q1: Should the digest agent be the listener group's own agent, or a
separate group?**
Simplest: same group. The agent has its group folder for notes. It reads
accumulated messages via `MessagesSince`. No new routing needed.

**Q2: Should per-message trigger suppression be absolute?**
Yes for now. A future `mode=selective` (route only messages matching a
pattern) is a separate feature.

**Q3: What happens if the digest run errors?**
Normal error handling: cursor rolls back, circuit breaker increments. Timed
will re-fire next scheduled slot. No special recovery needed.

**Q4: Can a listener group have child groups / routing?**
Not in this spec. Listener groups are terminal — no delegation, no prefix
routing. Enforced by skipping the routing resolution when `mode=listener`.
