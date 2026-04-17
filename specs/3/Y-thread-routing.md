---
status: shipped
---

# Thread-Aware Routing and Reply Chain Tracking

## Problem

Three related gaps:

1. **Reply chaining resets per container.** `lastSentID` is local to
   each container run. Multi-message conversations never thread.

2. **Telegram thread context is lost.** Topic captured on inbound but
   not used to constrain outbound replies to the same thread.

3. **@-routing doesn't preserve thread.** When @-mention routes to a
   subgroup, the reply loses thread context.

## Design

### 1. Persist last-sent message ID

`last_reply_id` column on `chats` table, keyed by `(chat_jid, topic)`.
Updated after every bot send. On next container spawn, read and use as
reply anchor. When steering messages arrive mid-run, the store is
updated immediately so the output callback picks up the new reply target.

Cursor advances on container COMPLETION via `advanceAgentCursor`, which
merges `max(batch timestamp, steered timestamp)` into a single cursor
write.

### 2. Thread ID as Topic — channel-agnostic

`Message.Topic` is the single thread identifier. Each adapter maps its
native threading concept to `Topic`:

| Adapter | Native concept  | Topic mapping                    |
| ------- | --------------- | -------------------------------- |
| teled   | MessageThreadID | `strconv.Itoa(threadID)` or `""` |
| discd   | thread/forum    | channel ID of thread or `""`     |
| whapd   | group topic     | topic string or `""`             |
| web     | URL topic slug  | already set correctly            |

Gateway already keys sessions on `(folder, topic)` — no changes needed
for inbound routing once adapters set Topic correctly.

Outbound: `Channel.Send(jid, text, replyTo, threadID string)` — gateway
passes `last.Topic` as `threadID`. Adapters that support threading use
it; others ignore it.

### 3. Reply-chain group routing

Resolution order (first match wins):

1. Inline `@name` in message content -> route to that group
2. `ReplyToID` present -> look up `routed_to` on that message -> route there
3. Sticky group set for this chat -> route there
4. Default group for this JID

`routed_to TEXT` column on `messages` table, set at bot-send time.

### Session continuity handles reply context

Per-thread session IDs are the reply context. `store.GetSession(folder,
topic)` returns a session ID per `(folder, topic)`. Once Topic maps to
the inbound thread ID, all messages share one session — Claude sees full
history on resume. Explicit `reply_to_text` threading is redundant for
the sessionized case.

## Implementation order

1. **Persist last-sent ID** — `last_reply_id` on chats, get/set methods
2. **Map native thread IDs to Topic** — per adapter, one field
3. **Thread-aware outbound** — `threadID` param on `Channel.Send`
4. **Reply-chain group routing** — `routed_to` on messages table
