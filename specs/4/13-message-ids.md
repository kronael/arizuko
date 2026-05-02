---
status: shipped
---

# Message IDs: Reply Threading

Capture channel-native reply IDs on inbound, thread outbound
replies through the same IDs.

## Problem

Without per-message IDs the agent cannot quote a specific
prior message or thread its outbound reply onto one. Quoted
text and sender name alone can't drive `Send(replyTo=...)`.

## Fields on `core.Message`

```go
type Message struct {
    // ...
    ForwardedFrom string // delegate/escalate return-routing JID; not channel forwards
    ReplyToID     string // channel-native replied-to msg ID
    ReplyToText   string // quoted text snippet
    ReplyToSender string // quoted sender name
    // ...
}
```

`ForwardedFrom` is reused as the return-address JID for
delegate/escalate (`gateway.delegateViaMessage`,
`escalate_group` MCP tool). It is not populated from inbound
forwarded messages on any platform — channel forward
metadata is not extracted today.

## Channel coverage — inbound reply metadata

| Channel  | reply_to_id  | reply_to_sender | reply_to_text |
| -------- | ------------ | --------------- | ------------- |
| Telegram | yes          | yes             | yes           |
| Discord  | yes          | yes             | yes           |
| WhatsApp | yes          | yes             | yes           |
| Mastodon | yes (status) | yes             | n/a           |
| Local    | yes (UUID)   | yes             | yes           |

## Outbound reply threading

Channels accept `replyTo` on `Send(jid, text, replyTo, threadID)`.

| Channel  | Mechanism                                      | Status   |
| -------- | ---------------------------------------------- | -------- |
| Telegram | `tgbotapi.NewMessage.ReplyToMessageID`         | shipped  |
| Discord  | `ChannelMessageSendReply` + `MessageReference` | shipped  |
| Mastodon | `toot.InReplyToID`                             | shipped  |
| Local    | `LocalChannel.Send` ReplyToID                  | shipped  |
| WhatsApp | quoted WAMessage object                        | deferred |

WhatsApp deferred: Baileys `sendMessage` requires the full
original `WAMessage`, not just an ID — needs message cache.

## Router XML

The reply pointer renders as a sibling header **above** its
`<message>`, with a consistent `id` attribute matching the message
naming convention. Self-closing when the parent text is omitted
(parent is in agent session); body-bearing when the gateway includes
an excerpt for out-of-session parents.

```xml
<messages>
  <reply-to id="789" sender="Alice"/>
  <message id="m1" sender="Bob" ...>response</message>
</messages>
```

Or with excerpt body:

```xml
<messages>
  <reply-to id="789" sender="Alice">quoted text</reply-to>
  <message id="m1" sender="Bob" ...>response</message>
</messages>
```

Emitted by `router.FormatMessages` whenever `ReplyToID != ""`. Body
is included when `ReplyToText != ""`. Sender defaults to `"unknown"`
when absent. The pointer precedes its `<message>` so the agent reads
"reply target" → "user reply" in order; structural prominence is the
intent signal that previous in-attribute `reply_to=` couldn't carry.

The legacy shape (`reply_to=` attribute on `<message>` plus inline
`<reply_to>` element) was retired at v0.33.3.

## DB schema

```sql
-- messages table (initial schema)
forwarded_from TEXT,
reply_to_text TEXT,
reply_to_sender TEXT,

-- migration 0003
ALTER TABLE messages ADD COLUMN reply_to_id TEXT;
```

## Not shipped — channel forward metadata

Inbound forward attribution (Telegram channel posts, Discord
forwards, WhatsApp forwarded flag) is not extracted into
`core.Message` today. No `ForwardedFromID` / `ForwardedMsgID`
fields, no DB columns, no `<forwarded_from>` XML tag. If
revisited:

- Telegram channel posts: `fwd.chat.id` + `fwd.message_id`
  are recoverable.
- Telegram user/hidden_user forwards: name only.
- Discord `MessageReferenceType.Forward`: no original sender exposed.
- WhatsApp `ctxInfo.isForwarded`: flag only.
- Email: thread-based; no per-message ID.
