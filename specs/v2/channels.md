# kanipi channel strategy

## Principle

No token → channel never loads. No explicit enable flags.

## Activation

| Channel  | Enabled when                             | JID prefix |
| -------- | ---------------------------------------- | ---------- |
| telegram | TELEGRAM_BOT_TOKEN set                   | `tg:`      |
| whatsapp | store/auth/creds.json exists (QR paired) | `wa:`      |
| discord  | DISCORD_BOT_TOKEN set                    | `discord:` |

```
# .env
TELEGRAM_BOT_TOKEN=123:ABC    # telegram active
DISCORD_BOT_TOKEN=MTIz...     # discord active
# whatsapp: pair via QR, auth files persist in store/auth/
```

## Channel interface

From types.ts — Channel defines the runtime interface,
ChannelOpts is the shared constructor argument:

```typescript
interface Channel {
  name: string;
  connect(): Promise<void>;
  sendMessage(jid: string, text: string): Promise<void>;
  isConnected(): boolean;
  ownsJid(jid: string): boolean;
  disconnect(): Promise<void>;
  setTyping?(jid: string, on: boolean): Promise<void>;
}

interface ChannelOpts {
  onMessage: OnInboundMessage;
  onChatMetadata: OnChatMetadata;
  registeredGroups: () => Record<string, RegisteredGroup>;
}
```

Each channel owns a JID prefix. `ownsJid()` returns true
for messages matching its prefix. Router iterates channels
to find the owner.

## Implementations

- `src/channels/telegram.ts` — grammy bot, long-polling,
  /chatid and /ping commands, @mention translation
- `src/channels/whatsapp.ts` — baileys, QR auth, LID
  translation, offline message queue, group metadata sync
- `src/channels/discord.ts` — discord.js, !chatid command,
  @mention translation, 2000 char message splitting

## v2: plugin loading

Currently all channels are compiled in and conditionally
instantiated. Future: dynamic import so unused channel
dependencies aren't loaded at all.

## Legacy (removed)

- `TELEGRAM_ONLY` flag — replaced by token-presence toggling
- nanoclaw `add-telegram`/`add-discord` skills — source
  modifiers, replaced by built-in channels
