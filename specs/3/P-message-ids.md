---
status: spec
---

# Message IDs: Reply and Forward Metadata

Enrich inbound message metadata with channel-native IDs for reply threading
and forward attribution. Mirrors the reply_msgid work in 5-permissions.

## Problem

`NewMessage` captures reply context as plain text (`reply_to_text`,
`reply_to_sender`) and forward source as a name string (`forwarded_from`).
No IDs are stored. Agents cannot thread replies to specific messages and
cannot reference original forwarded sources.

## Fields to add to `NewMessage`

```typescript
export interface NewMessage {
  // existing
  forwarded_from?: string; // original sender name
  reply_to_text?: string; // quoted message text (100 chars)
  reply_to_sender?: string; // quoted message sender name
  // new
  reply_to_id?: string; // channel-native ID of the replied-to message
  forwarded_from_id?: string; // source chat/channel ID (where available)
  forwarded_msgid?: string; // original message ID (channel posts only)
}
```

## Channel coverage

### Reply IDs (`reply_to_id`)

| Channel  | Source                                    | Available                                  |
| -------- | ----------------------------------------- | ------------------------------------------ |
| Telegram | `ctx.message.reply_to_message.message_id` | yes                                        |
| Discord  | `msg.reference.messageId`                 | yes                                        |
| WhatsApp | `ctxInfo.stanzaId`                        | yes                                        |
| Mastodon | `status.inReplyToId`                      | yes (already in `replyTo` on `NewMessage`) |
| Email    | thread-based, no per-message ID           | n/a                                        |

### Forward IDs (`forwarded_from_id`, `forwarded_msgid`)

| Channel  | Source                                                                | Available                  |
| -------- | --------------------------------------------------------------------- | -------------------------- |
| Telegram | `forward_origin.type === 'channel'`: `fwd.chat.id` + `fwd.message_id` | yes                        |
| Telegram | `forward_origin.type === 'user'` / `'hidden_user'`                    | no original ID             |
| Discord  | `MessageReferenceType.Forward`                                        | no sender metadata exposed |
| WhatsApp | `ctxInfo.isForwarded = true`                                          | no original source         |

Only Telegram channel posts carry a recoverable origin ID.
For other forward types, `forwarded_from` name string is sufficient.

## Router XML

Current: `<forwarded_from sender="..."/>` (no ID). Keep existing tag name.

Updated:

```xml
<!-- simple forward — name only -->
<forwarded_from sender="John"/>

<!-- channel post forward — with source -->
<forwarded_from sender="Tech News" chat="telegram:-100123456" id="456"/>

<!-- reply — already correct, add id -->
<reply_to sender="Alice" id="789">quoted message text…</reply_to>
```

`id` on both tags is the channel-native message ID. Omit if absent.
`chat` / `id` on `<forwarded_from>` only when both are present (Telegram channel posts).

## DB schema

Add columns to `messages` table via migration:

```sql
ALTER TABLE messages ADD COLUMN reply_to_id TEXT;
ALTER TABLE messages ADD COLUMN forwarded_from_id TEXT;
ALTER TABLE messages ADD COLUMN forwarded_msgid TEXT;
```

Update `storeMessage` and `getNewMessages` to include the new columns.

## send_message: reply threading

`send_message` action gains optional `replyTo?: string`.
`ActionContext.sendMessage` signature: `(jid, text, opts?: SendOpts)`.
Agents pass the `reply_to_id` or `reply_msgid` from session context.

Channel implementations of `sendMessage(jid, text, opts)`:

| Channel  | Implementation                                                                        | Status   |
| -------- | ------------------------------------------------------------------------------------- | -------- |
| Telegram | `reply_parameters: { message_id: Number(opts.replyTo) }`                              | done     |
| Discord  | `channel.send({ content, reply: { messageReference: { messageId: opts.replyTo } } })` | todo     |
| WhatsApp | needs quoted message object — deferred                                                | deferred |
| Mastodon | `client.reply(opts.replyTo, text)` stub                                               | verify   |
| Reddit   | `client.reply(opts.replyTo, text)` stub                                               | verify   |
| Email    | `In-Reply-To` header — already thread-based                                           | n/a      |

## Required changes

- `src/types.ts`: add `reply_to_id?`, `forwarded_from_id?`, `forwarded_msgid?` to `NewMessage`
- `src/migrations/0009-message-ids.sql`: ALTER TABLE for three new columns
- `src/db.ts` `storeMessage` + `getNewMessages`: include new columns
- `src/channels/telegram.ts`: extract `reply_to_id` from `reply_to_message.message_id`;
  extract `forwarded_from_id` + `forwarded_msgid` from `forward_origin.type === 'channel'`
- `src/channels/discord.ts`: extract `reply_to_id` from `msg.reference.messageId`;
  implement `sendMessage` reply via `channel.send({ reply: ... })`
- `src/channels/whatsapp.ts`: extract `reply_to_id` from `ctxInfo.stanzaId`
- `src/router.ts` `formatMessages`: add `id` attr to `<reply_to>`;
  add `chat`/`id` attrs to `<forwarded_from>` when present
- `src/actions/messaging.ts` `send_message`: add `replyTo?: string` field
- `src/action-registry.ts` `ActionContext.sendMessage`: add `opts?: SendOpts`
- `src/ipc.ts`: wire `SendOpts` through to channel `sendMessage` calls

## Open questions

- **WhatsApp reply**: Baileys `sendMessage` for quoted messages requires
  `{ quoted: WAMessage }` — the full original message object, not just an ID.
  Would need to fetch the message from history or cache it. Deferred.
- **Discord forward metadata**: `MessageReferenceType.Forward` doesn't expose
  the original sender. Current `'(forwarded)'` string is best we can do.
- **Mastodon/Reddit stubs**: need integration test to verify `client.reply()` API.
