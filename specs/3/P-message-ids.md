---
status: partial
---

# Message IDs: Reply and Forward Metadata

Enrich inbound messages with channel-native IDs for reply threading
and forward attribution.

## New fields on NewMessage

- `reply_to_id` — channel-native ID of replied-to message
- `forwarded_from_id` — source chat/channel ID
- `forwarded_msgid` — original message ID (channel posts only)

## reply_to_id coverage

| Channel  | Source                          | Available |
| -------- | ------------------------------- | --------- |
| Telegram | `reply_to_message.message_id`   | yes       |
| Discord  | `msg.reference.messageId`       | yes       |
| WhatsApp | `ctxInfo.stanzaId`              | yes       |
| Mastodon | `status.inReplyToId`            | yes       |
| Email    | thread-based, no per-message ID | n/a       |

## Forward coverage

| Channel  | Source                                          | Available                  |
| -------- | ----------------------------------------------- | -------------------------- |
| Telegram | channel posts: `fwd.chat.id` + `fwd.message_id` | yes                        |
| Telegram | user/hidden_user forwards                       | no original ID             |
| Discord  | `MessageReferenceType.Forward`                  | no sender metadata exposed |
| WhatsApp | `ctxInfo.isForwarded = true`                    | no original source         |

Only Telegram channel posts carry a recoverable origin ID.

## XML

```xml
<forwarded_from sender="Tech News" chat="telegram:-100..." id="456"/>
<reply_to sender="Alice" id="789">quoted text</reply_to>
```

`id` is channel-native. Omit if absent. `<forwarded_from>` `chat`/`id`
only when both present (Telegram channel posts).

## send_message replyTo

Optional `replyTo?: string`. Agent passes `reply_to_id` from context.

| Channel  | Implementation                     | Status   |
| -------- | ---------------------------------- | -------- |
| Telegram | `reply_parameters: { message_id }` | done     |
| Discord  | `message.reply()`                  | todo     |
| WhatsApp | needs quoted message object        | deferred |
| Mastodon | `client.reply(id, text)` stub      | verify   |
| Email    | `In-Reply-To` header, thread-based | n/a      |

## WhatsApp limitation

Baileys `sendMessage` requires `{ quoted: WAMessage }` — the full
original object, not just an ID. Would need history/cache fetch.
Deferred.
