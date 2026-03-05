# kanipi channel strategy — shipped

## Principle

No token → channel never loads. No explicit enable flags.

## Activation

| Channel  | Enabled when                               | JID prefix |
| -------- | ------------------------------------------ | ---------- |
| telegram | `TELEGRAM_BOT_TOKEN` set                   | `tg:`      |
| whatsapp | `store/auth/creds.json` exists (QR paired) | `wa:`      |
| discord  | `DISCORD_BOT_TOKEN` set                    | `discord:` |
| email    | `EMAIL_IMAP_HOST` set                      | `email:`   |

## Channel interface

```typescript
interface Channel {
  name: string;
  connect(): Promise<void>;
  disconnect(): Promise<void>;
  isConnected(): boolean;
  ownsJid(jid: string): boolean;
  sendMessage(jid: string, text: string, opts?: SendOpts): Promise<void>;
  setTyping?(jid: string, on: boolean): Promise<void>;
  sendDocument?(
    jid: string,
    filePath: string,
    filename?: string,
  ): Promise<void>;
}

interface SendOpts {
  replyTo?: string; // channel-native thread/reply handle
}
```

`ownsJid()` returns true for JIDs with the channel's prefix. Router iterates
channels to find the owner for each outbound message.

## Inbound callbacks

```typescript
// Delivers an inbound message to the gateway.
// attachments + download are set for media entering the enricher pipeline.
type OnInboundMessage = (
  chatJid: string,
  message: NewMessage,
  attachments?: RawAttachment[],
  download?: AttachmentDownloader,
) => void;

// Reports chat metadata (name, channel, group flag).
// Channels that deliver names inline (Telegram) pass name here.
// Channels that sync separately (WhatsApp) omit it.
type OnChatMetadata = (
  chatJid: string,
  timestamp: string,
  name?: string,
  channel?: string,
  isGroup?: boolean,
) => void;

interface ChannelOpts {
  onMessage: OnInboundMessage;
  onChatMetadata: OnChatMetadata;
  registeredGroups: () => Record<string, RegisteredGroup>;
}
```

## Threading

Threading is a first-class channel concern. When a user messages inside a
thread or replies to a specific message, the channel should:

1. Populate `NewMessage.replyTo` with the channel-native handle.
2. Pass `opts.replyTo` to `sendMessage` so the response lands in the same thread.

Channel-native handles:

| Channel  | `replyTo` value                 | `sendMessage` behaviour      |
| -------- | ------------------------------- | ---------------------------- |
| telegram | `message_id` as string          | `reply_to_message_id`        |
| discord  | thread channel ID or message ID | send into thread / reply     |
| whatsapp | quoted message key              | `quoted` field in Baileys    |
| email    | root `Message-ID` header value  | `In-Reply-To` + `References` |

`NewMessage.replyTo?: string` field not yet added — open.
`sendMessage` opts not yet wired in implementations — open.

### Current state

Email is the only channel with threading implemented, via `email_threads` in
`db.ts` (`message_id → thread_id → root_msg_id`). All other channels start
new conversations on every reply.

`email_threads` should be generalised to a `channel_threads` table keyed by
`(channel, thread_id)` once a second channel implements threading.

## Implementations

| Channel  | File                       | Notes                                                        |
| -------- | -------------------------- | ------------------------------------------------------------ |
| telegram | `src/channels/telegram.ts` | grammy, long-poll, /chatid + /ping, @mention translation     |
| whatsapp | `src/channels/whatsapp.ts` | baileys, QR auth, LID translation, offline queue, group sync |
| discord  | `src/channels/discord.ts`  | discord.js, !chatid, @mention translation, 2000-char split   |
| email    | `src/channels/email.ts`    | IMAP IDLE + SMTP, threading via email_threads table          |

## Optional interface coverage

| Method         | telegram | whatsapp | discord | email |
| -------------- | -------- | -------- | ------- | ----- |
| `setTyping`    | ✓        | ✓        | ✗       | ✗     |
| `sendDocument` | ✓        | ✓        | ✓       | ✗     |
| threading      | ✗ open   | ✗ open   | ✗ open  | ✓     |

## v2: plugin loading

Currently all channels are compiled in and conditionally instantiated.
Future: dynamic import so unused channel dependencies aren't loaded at all.
