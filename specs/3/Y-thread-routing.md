---
status: shipped
---

# Thread-Aware Routing and Reply Chain Tracking

## Problems addressed

1. **Reply chaining reset per container** — `lastSentID` was local to
   each run. Multi-message conversations never threaded.
2. **Telegram thread context lost** — topic captured on inbound but not
   used to constrain outbound replies.
3. **@-routing lost thread** — when @-mention routed to a subgroup,
   reply lost thread context.

## Design

### 1. Persist last-sent ID

`last_reply_id` column on `chats`, keyed by `(chat_jid, topic)`. Updated
after every bot send. Next spawn reads and uses as reply anchor. Steering
messages update the store immediately so the output callback picks up
the new target.

Cursor advances on container completion via `advanceAgentCursor`, merging
`max(batch, steered timestamp)` into a single cursor write.

### 2. Thread ID as Topic

`Message.Topic` is the single thread identifier. Adapters map native:

| Adapter | Native concept  | Topic mapping                    |
| ------- | --------------- | -------------------------------- |
| teled   | MessageThreadID | `strconv.Itoa(threadID)` or `""` |
| discd   | thread/forum    | channel ID of thread or `""`     |
| whapd   | group topic     | topic string or `""`             |
| web     | URL topic slug  | already correct                  |

Gateway keys sessions on `(folder, topic)`.

Outbound: `Channel.Send(jid, text, replyTo, threadID string)` — gateway
passes `last.Topic` as `threadID`.

### 3. Reply-chain group routing

Resolution order (first match wins):

1. Inline `@name` in content → that group
2. `ReplyToID` present → look up `routed_to` on that message
3. Sticky group set → that group
4. Default group for this JID

`routed_to TEXT` column on `messages`, set at bot-send time.

### Session continuity

Per-thread session IDs ARE the reply context. `store.GetSession(folder,
topic)` returns per `(folder, topic)`. Once Topic maps to inbound thread
ID, messages share one session. Explicit `reply_to_text` threading is
redundant for the sessionized case.

## Implementation order

1. Persist last-sent ID (`last_reply_id` on chats)
2. Map native thread IDs to Topic per adapter
3. Thread-aware outbound (`threadID` on `Channel.Send`)
4. Reply-chain group routing (`routed_to` on messages)
