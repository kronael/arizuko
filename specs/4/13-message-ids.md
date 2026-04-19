---
status: shipped
---

# Message IDs: Reply and Forward Metadata

Enrich inbound message metadata with channel-native IDs for
reply threading and forward attribution.

## Problem

Inbound messages capture reply context as plain text and
forward source as a name string. No IDs stored. Agents
cannot thread replies or reference forwarded sources.

## Fields on inbound message

```go
type Message struct {
    // existing
    ForwardedFrom   string // original sender name
    ReplyToText     string // quoted text (100 chars)
    ReplyToSender   string // quoted sender name
    // added
    ReplyToID       string // channel-native replied-to msg ID
    ForwardedFromID string // source chat/channel ID
    ForwardedMsgID  string // original message ID (channel posts)
}
```

Note: `ForwardedFromID` and `ForwardedMsgID` are NOT yet in `core.Message`.
Only `ForwardedFrom string` exists. `ReplyToID` is present.

## Channel coverage — reply/forward metadata

Full matrix of what each channel provides for inbound
metadata extraction.

### Reply metadata

| Channel  | reply_to_id  | reply_to_sender | reply_to_text |
| -------- | ------------ | --------------- | ------------- |
| Telegram | yes          | yes             | yes           |
| Discord  | yes          | yes             | yes           |
| WhatsApp | yes          | yes             | yes           |
| Email    | n/a (thread) | yes (From)      | n/a           |
| Local    | yes (UUID)   | yes             | yes           |

### Forward metadata

| Channel  | fwd_from (name) | fwd_from_id (chat) | fwd_msgid |
| -------- | --------------- | ------------------ | --------- |
| Telegram | yes             | channel posts only | chan only |
| Discord  | "(forwarded)"   | no                 | no        |
| WhatsApp | yes (flag only) | no                 | no        |
| Email    | n/a             | n/a                | n/a       |
| Local    | yes             | yes                | yes       |

Only Telegram channel posts carry recoverable origin ID.
For other forward types, name string is sufficient.

### Outbound reply threading

| Channel  | Mechanism                          | Status   |
| -------- | ---------------------------------- | -------- |
| Telegram | reply_parameters.message_id        | shipped  |
| Discord  | MessageReference                   | shipped  |
| WhatsApp | quoted WAMessage object (deferred) | deferred |
| Email    | In-Reply-To header                 | n/a      |
| Local    | replyTo field on store             | shipped  |

## Router XML

```xml
<forwarded_from sender="John"/>
<forwarded_from sender="News" chat="telegram:-100123" id="456"/>
<reply_to sender="Alice" id="789">quoted text</reply_to>
```

`id` is channel-native message ID. Omit if absent.
`chat`/`id` on `<forwarded_from>` only when both present.

## DB schema

```sql
ALTER TABLE messages ADD COLUMN reply_to_id TEXT;
ALTER TABLE messages ADD COLUMN forwarded_from_id TEXT;
ALTER TABLE messages ADD COLUMN forwarded_msgid TEXT;
```

## Deferred

- WhatsApp reply: Baileys requires full WAMessage object,
  not just ID. Needs message cache. Deferred.
- Discord forward: MessageReferenceType.Forward exposes no
  original sender. "(forwarded)" string is best available.
