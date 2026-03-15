## <!-- trimmed 2026-03-15: TS removed, acceptance criteria removed, rich facts only -->

## status: spec

# Message IDs: Reply and Forward Metadata

Enrich inbound messages with channel-native IDs for reply threading
and forward attribution.

## New Fields on NewMessage

- `reply_to_id` -- channel-native ID of replied-to message
- `forwarded_from_id` -- source chat/channel ID (where available)
- `forwarded_msgid` -- original message ID (channel posts only)

## Channel Coverage: reply_to_id

| Channel  | Source                          | Available |
| -------- | ------------------------------- | --------- |
| Telegram | `reply_to_message.message_id`   | yes       |
| Discord  | `msg.reference.messageId`       | yes       |
| WhatsApp | `ctxInfo.stanzaId`              | yes       |
| Mastodon | `status.inReplyToId`            | yes       |
| Email    | thread-based, no per-message ID | n/a       |

## Channel Coverage: forward IDs

| Channel  | Source                                          | Available                  |
| -------- | ----------------------------------------------- | -------------------------- |
| Telegram | channel posts: `fwd.chat.id` + `fwd.message_id` | yes                        |
| Telegram | user/hidden_user forwards                       | no original ID             |
| Discord  | `MessageReferenceType.Forward`                  | no sender metadata exposed |
| WhatsApp | `ctxInfo.isForwarded = true`                    | no original source         |

Only Telegram channel posts carry recoverable origin ID.

## Updated XML Format

```xml
<forwarded_from sender="Tech News" chat="telegram:-100123456" id="456"/>
<reply_to sender="Alice" id="789">quoted message text</reply_to>
```

`id` on both tags is channel-native. Omit if absent. `chat`/`id` on
`<forwarded_from>` only when both present (Telegram channel posts).

## send_message replyTo

`send_message` gains optional `replyTo?: string`. Agents pass the
`reply_to_id` from session context.

| Channel  | Implementation                     | Status   |
| -------- | ---------------------------------- | -------- |
| Telegram | `reply_parameters: { message_id }` | done     |
| Discord  | `message.reply()`                  | todo     |
| WhatsApp | needs quoted message object        | deferred |
| Mastodon | `client.reply(id, text)` stub      | verify   |
| Email    | `In-Reply-To` header, thread-based | n/a      |

## Open: WhatsApp Reply Limitation

Baileys `sendMessage` requires `{ quoted: WAMessage }` -- the full
original message object, not just an ID. Would need to fetch from
history or cache. Deferred.
