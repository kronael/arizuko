---
status: shipped
---

# Turn Retry on Failed Completion

When an agent turn dies mid-execution (SIGKILL, OOM, timeout) without
delivering a reply, reschedule a follow-up turn with retry semantics.

## Problem

A turn that gets:

- Container killed (exit 137 = SIGKILL, typically OOM)
- Timeout exceeded (RUNED_RUN_TIMEOUT, default 30m)
- Transport failure (routd→runed blip)

...never reaches `submit_turn` or `reply`. The user sees silence. Currently:

- routd marks the turn as failed in turn_context
- The message stays in the queue but never re-dispatches (guard prevents
  re-feeding a completed/failed turn)
- User must re-mention to trigger a new turn

## Design (decisions locked)

**Automatic retry** with bounded attempts:

1. When a turn ends without reply (no `submit_turn` call, no `reply` tool),
   routd detects this in `recordTurnResult` (already tracks `TurnHasBotReply`)
2. If `retry_count < MAX_TURN_RETRY` (default 3), reschedule the same message
   for a new turn after a short backoff
3. Increment `retry_count` on the turn_context row
4. On final retry (attempt 3), if still no reply, deliver an error notice
   to the user: "⚠️ Agent couldn't complete this request after 3 attempts."

**Backoff**: 10 seconds between retries. Always retry immediately after
detecting failure — no extended delays. The old container dies naturally;
the new one is a fresh spawn.

**System note on retry**: Inject a system message into the retry turn's
context: "This is retry attempt N of 3. The previous attempt was killed
before completing. Be conservative with resource usage."

**User visibility**: Silent until final failure. From the user's perspective,
this is just a normal (slightly longer) agent reply. Only surface an error
if all retries fail.

**What gets retried**:

- The SAME input message (user's text + attachments)
- A FRESH container (new spawn, no stale state from crashed run)
- The SAME conversation context (history load works as normal)

**What doesn't retry**:

- Turns that DID reply (success = bot message exists in messages table)
- Turns where the agent explicitly errored via `submit_turn` with error flag
- Turns killed by user cancel (if we add that)

**Partial reply clarification**: If the agent posted a `reply` to the channel
but died before `submit_turn`, `TurnHasBotReply` sees the bot row → success,
no retry. A partial reply that reached the channel is better than nothing.
If no reply reached the channel at all, that's a failure → retry.

## Configuration

Global only: `MAX_TURN_RETRY` in `.env` (default 3). No per-folder or
per-skill overrides — keep it simple.

## Engagement TTL

Set `ENGAGEMENT_TTL` to 60m (up from 30m default). Retry delays (10s × 3)
are negligible compared to TTL. Retry does NOT need to extend engagement
because from the user's perspective, the turn is still "in progress" —
they're waiting for a reply, engagement is naturally alive.

## Schema change

```sql
ALTER TABLE turn_context ADD COLUMN retry_count INTEGER DEFAULT 0;
```

## Implementation sketch

```go
// routd/turns.go, in recordTurnResult after detecting no reply:
if !hasBotReply && tc.RetryCount < cfg.MaxTurnRetry {
    tc.RetryCount++
    tc.Status = "pending_retry"
    s.db.UpdateTurnContext(tc)
    time.AfterFunc(10*time.Second, func() {
        s.requeueTurn(tc.MessageID, tc.RetryCount)
    })
    slog.Warn("turn failed without reply, scheduling retry",
        "message_id", tc.MessageID, "retry", tc.RetryCount)
    return
}
if !hasBotReply {
    // final failure: deliver error notice
    s.deliverErrorNotice(tc, "Agent couldn't complete this request after 3 attempts.")
}
```

```go
// In prompt building, inject retry note:
if retryCount > 0 {
    systemNotes = append(systemNotes, fmt.Sprintf(
        "This is retry attempt %d of %d. The previous attempt was killed "+
        "before completing. Be conservative with resource usage.",
        retryCount+1, cfg.MaxTurnRetry))
}
```

## Related

- `bugs.md` 2026-06-14 "deep-dive replies cut off" — the motivating incident
- `specs/5/G-engagement.md` — engagement TTL (increase to 60m)
- `specs/5/P-runed.md` — container lifecycle, RunTTL
