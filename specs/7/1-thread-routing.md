# Thread-Aware Routing and Reply Chain Tracking

## Problem

Three related gaps:

1. **Reply chaining resets per container.** `lastSentID` in `makeOutputCallback`
   is local — each container run starts the reply chain from the original user
   message. Multi-message conversations never thread.

2. **Telegram thread (topic) context is lost.** In Telegram supergroups with
   topics enabled, each message belongs to a `MessageThreadID`. We capture
   `Topic` on inbound messages but don't use it to constrain outbound replies
   to the same thread. Bot replies land in the wrong thread.

3. **@-routing doesn't preserve thread.** When a user @-mentions an agent name
   to route to a different group, the reply goes back to the routed group with
   no thread context. If the original message was in thread X, the reply should
   also land in thread X.

---

## Design

### 1. Persist last-sent message ID

Add to store:

```sql
-- in chats table or a new table
ALTER TABLE chats ADD COLUMN last_reply_id TEXT NOT NULL DEFAULT '';
```

`last_reply_id` is the message ID of the last message in this thread — either
a bot reply or the most recent user message. On send: write the sent ID. On
next container spawn: read and use as reply anchor. This keeps the reply chain
continuous regardless of who sent last.

Schema already has `topic` on messages — use `(chat_jid, topic)` as the key
since each topic is an independent thread.

#### Store changes

```go
// store.go
SetLastReplyID(chatJID, topic, msgID string)
GetLastReplyID(chatJID, topic string) string
```

#### Gateway changes

In `makeOutputCallback`, restore `lastSentID` from store instead of starting
from `firstMsgID`:

```go
func (g *Gateway) makeOutputCallback(chatJid, topic, firstMsgID string) (func(string, string), *bool) {
    var hadOutput bool
    // Restore from store; fall back to user's message if none
    lastSentID := g.store.GetLastReplyID(chatJid, topic)
    if lastSentID == "" {
        lastSentID = firstMsgID
    }
    return func(text, _ string) {
        if text == "" { return }
        hadOutput = true
        stripped, statuses := router.ExtractStatusBlocks(text)
        for _, s := range statuses { g.sendMessage(chatJid, s) }
        if clean := router.FormatOutbound(stripped); clean != "" {
            if sentID, _ := g.sendMessageReply(chatJid, clean, lastSentID); sentID != "" {
                lastSentID = sentID
                g.store.SetLastReplyID(chatJid, topic, sentID)
            }
        }
    }, &hadOutput
}
```

Pass `last.Topic` when calling `makeOutputCallback`.

---

### 2. Thread ID as Topic — channel-agnostic

`Message.Topic` is the single thread identifier in the system. The gateway
uses it uniformly for session scoping, reply tracking, and routing. Channels
should not need special-casing anywhere in the gateway.

**Contract for channel adapters:**

Each adapter maps its native threading concept to `Topic` on inbound messages.
That is one field, set in one place per adapter:

| Adapter | Native concept       | → `Topic`                        |
| ------- | -------------------- | -------------------------------- |
| teled   | `MessageThreadID`    | `strconv.Itoa(threadID)` or `""` |
| discd   | Discord thread/forum | channel ID of thread or `""`     |
| whapd   | WhatsApp group topic | topic string or `""`             |
| web     | URL topic slug       | already set correctly            |

No gateway changes needed for inbound routing — it already keys sessions on
`(folder, topic)`.

**Outbound: keeping replies in the thread**

`Topic` is stored on every message in the DB. The gateway already has
`last.Topic` at reply time — it just needs to pass it to `Channel.Send`.

Add `threadID` to `Channel.Send`:

```go
Send(jid, text, replyTo, threadID string) (string, error)
```

The gateway passes `last.Topic` as `threadID`. Adapters that support
threading use it (teled → `MessageThreadID`); others ignore it. Because
`Topic` lives in the messages table, disparate sources (web, Telegram,
WhatsApp) sharing a topic slug naturally land in the same logical thread —
the adapter maps Topic back to its own native thread concept on send.

---

### 3. @-routing preserves thread context

Current @-routing: when the router matches an @-mention pattern and routes to
group `target`, the reply comes from that group's agent with no thread context.

Fix: pass the original message's `ChatJID`, `Topic`, and `ReplyToID` through
the routing context. The target group's output callback sends back to the
_original_ JID+thread.

```go
// In gateway routing for @-mention:
// instead of sending to the target group's JID, override the reply destination
// to the source chatJID + topic.
```

This requires a `SourceJID` + `SourceTopic` concept when doing cross-group
routing. The target group agent runs, result is delivered back to `SourceJID`
in `SourceTopic`.

The current routing model doesn't have this — the target group's `chatJid` is
used for delivery. Extend `runAgentWithOpts` or the routing path to accept an
optional `replyJID` + `replyTopic` override.

---

### Reply-to context: session continuity is enough

**Key insight (from takopi):** Per-thread session IDs are the reply context.

takopi maps each `(chat_id, thread_id)` to a `TopicStateStore` which holds a
session resume token per engine. Every message in that Telegram thread resumes
the same Claude session. Claude's conversation history _is_ the thread context
— no need to extract and re-inject `reply_to_text`. The resumed session already
contains every prior exchange.

Arizuko has the same mechanism: `store.GetSession(folder, topic)` returns a
session ID per `(folder, topic)`. When `Topic` correctly maps to the inbound
Telegram `MessageThreadID`, all messages in a forum thread share one session.
Claude sees the full history on resume.

**Consequence:** Step 2 (capture Telegram thread ID → `Topic`) is the critical
piece. Once `Topic` = thread ID, session continuity handles reply context for
free. Explicitly threading `reply_to_text` through the prompt is redundant.

The `reply_to_text` field remains useful for non-sessionized cases (e.g., first
message in a thread where no session exists yet, or cross-group routing where
source context isn't in the target group's session). But it is not the primary
mechanism.

---

## Implementation order

1. **Persist last-sent ID** — self-contained, fixes reply chaining now.
   - `store.go`: add `last_reply_id` to chats, add get/set methods
   - `gateway.go`: restore from store in `makeOutputCallback`, pass topic
   - Migration: `ALTER TABLE chats ADD COLUMN last_reply_id TEXT NOT NULL DEFAULT ''`

2. **Capture Telegram thread ID inbound** — teled only, no interface change.
   - Store `MessageThreadID` as `Topic` on inbound messages

3. **Thread-aware outbound** — Channel interface change, all adapters.
   - Add `threadID` param to `Channel.Send`
   - teled sets `MessageThreadID` on outbound messages
   - Gateway passes `topic` from last inbound message as thread ID

4. **@-routing thread preservation** — requires (3) above.
   - Add `replyJID`/`replyTopic` override to routing path

---

## Schema migration

```sql
-- NNN-last-reply-id.sql
ALTER TABLE chats ADD COLUMN last_reply_id TEXT NOT NULL DEFAULT '';
```

Migration version bump + SKILL.md update per shipping checklist.
