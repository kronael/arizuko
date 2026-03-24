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

`last_reply_id` is the Telegram message ID of the last bot reply to this chat
(or chat+topic). On send: write. On next container spawn: read and pass in.

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

### 2. Telegram thread ID on outbound

Telegram threads: a supergroup topic has a `MessageThreadID` equal to the
root message ID of that topic. To reply within a thread, set both:

- `m.ReplyToMessageID` (for reply chain)
- `m.MessageThreadID` (to stay in the topic)

`Channel.Send` signature is `Send(jid, text, replyTo string) (string, error)`.

**Option A**: encode thread ID in the JID: `telegram:-1001234567890:42` where
`42` is the thread ID. Pros: no interface change. Cons: JID semantics are
muddied; all channel types must handle the suffix.

**Option B**: add `threadID` to the Channel interface:

```go
Send(jid, text, replyTo, threadID string) (string, error)
```

Pros: explicit. Cons: every channel adapter needs updating (discd, mastd, etc.
can just ignore it).

**Recommendation: Option B.** Cleaner. All current adapters set `threadID = ""`
(no-op). Telegram uses it.

#### teled change

```go
func (b *bot) send(jid, text, replyTo, threadID string) (string, error) {
    // ...
    m := tgbotapi.NewMessage(id, c)
    if replyMsgID != 0 {
        m.ReplyToMessageID = replyMsgID
    }
    if threadID != "" {
        if tid, err := strconv.ParseInt(threadID, 10, 64); err == nil {
            m.MessageThreadID = int(tid)
        }
    }
    // ...
}
```

#### Inbound thread ID capture

In teled, on receive: capture `msg.MessageThreadID` and store as `Topic`.
Currently `Topic` is set from another source — verify it's the Telegram thread
ID or add a separate field.

```go
// teled/bot.go on message receive:
topic := ""
if msg.MessageThreadID != 0 {
    topic = strconv.Itoa(msg.MessageThreadID)
}
```

This is consistent with existing `Message.Topic` usage for web.

#### Gateway outbound with thread

When sending a reply, the gateway needs to know the thread ID. This comes from
the inbound message's `Topic`. Pass it through to `sendMessageReply`:

```go
g.sendMessageReplyInThread(chatJid, clean, lastSentID, threadID)
```

Or encode into the JID (Option A above). Given Option B is selected, add
`threadID` to the send path.

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
