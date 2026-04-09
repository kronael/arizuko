---
status: shipped
---

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

`makeOutputCallback` initializes `lastSentID` to `firstMsgID` (the user
message that triggered the agent run). Before each send, it re-reads
`GetLastReplyID` from the store — if a steering message arrived mid-run,
the store will have the steering message's ID, and the next bot chunk
replies to that instead.

When `queue.SendMessages` succeeds (steering a follow-up batch into a
running container), the gateway immediately calls `SetLastReplyID` with
the steering message's ID. This way the output callback picks up the
new reply target on its next chunk. The gateway must also advance
`agentCursor` for the steered batch — otherwise `drainGroupLocked` sees
those messages as unprocessed after the container exits and respawns
with the same batch, causing Claude to see them twice via session
resume.

Delivery is not instant: the container-side receives steered text at
the next delivery boundary, not mid-text. Primary path is a PostToolUse
hook that injects `additionalContext` between tool calls (mid-loop);
fallback for text-only responses is `pollIpcDuringQuery` calling
`stream.push`, which lands on the next turn.

```go
// Poll loop — steering path
texts := make([]string, len(chatMsgs))
for i, m := range chatMsgs {
    texts[i] = m.Content
}
if g.queue.SendMessages(chatJid, texts) {
    g.store.SetLastReplyID(chatJid, topic, last.ID)
    g.advanceAgentCursor(chatJid, chatMsgs)
    ...
}

// Output callback — reads latest reply target before each send
replyTarget := func() string {
    if id := g.store.GetLastReplyID(chatJid, topic); id != "" {
        return id
    }
    return lastSentID
}
```

After each successful bot send, `SetLastReplyID` is updated to the
bot's sent message ID, so IPC tool calls and subsequent chunks chain
correctly.

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

### 3. Reply-chain group routing

When a user replies to a bot message that was produced by @subgroup routing,
the reply should continue going to the same subgroup — not the default group.

**Minimal approach:** the router already resolves the target group. Add one
more source to that resolution: the reply chain.

Resolution order (first match wins):

1. Inline `@name` in the message content → route to that group
2. `ReplyToID` present → look up which group handled that message → route there
3. Sticky group set for this chat → route there
4. Default group for this JID

For (2): add `routed_to TEXT` to the `messages` table. When a bot message is
stored (at send time), set `routed_to = groupFolder`. Router looks it up:

```sql
SELECT routed_to FROM messages WHERE id = ? AND routed_to != ''
```

No separate table — routing metadata lives on the message itself.

No SourceJID/SourceTopic override needed in the gateway. The router resolves
the group; the gateway runs it and replies to the original chatJID+topic as
always. Each component keeps its own concern.

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

2. **Map native thread IDs to Topic** — per adapter, one field.
   - teled: `MessageThreadID` → `Topic`
   - discd: thread/channel ID → `Topic`
   - Gateway is already channel-agnostic once this is done

3. **Thread-aware outbound** — Channel interface change, all adapters.
   - Add `threadID` param to `Channel.Send`
   - Gateway passes `last.Topic` as `threadID` on every send
   - Adapters that support threads use it; others ignore it

4. **Reply-chain group routing** — router change only.
   - Record `(msg_id, group_folder)` for each bot message sent
   - Router resolution: inline @name → reply chain lookup → sticky → default

---

## Schema migration

```sql
-- NNN-last-reply-id.sql
ALTER TABLE chats ADD COLUMN last_reply_id TEXT NOT NULL DEFAULT '';
```

Migration version bump + SKILL.md update per shipping checklist.
