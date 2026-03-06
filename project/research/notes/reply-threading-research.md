# Reply Threading Research — Telegram & WhatsApp in arizuko

## Scope

Researched how inbound reply context (quoted messages, replyTo, stanzaId) is
captured and forwarded to the agent in arizuko (nanoclaw fork). Also surveyed
what "brainpro" does — result: the only public project named brainpro
(github.com/jgarzik/brainpro) is a Rust agentic coding assistant with no
messaging channels. The arizuko codebase itself is the primary subject.

---

## 1. Current State in arizuko

### 1.1 `NewMessage` interface (`src/types.ts`)

```ts
export interface NewMessage {
  id: string;
  chat_jid: string;
  sender: string;
  sender_name?: string;
  content: string;
  timestamp: string;
  is_from_me?: boolean;
  is_bot_message?: boolean;
}
```

**No `reply_to` / `quoted_message` / `stanza_id` field exists.** The interface
captures the message but discards all reply-context metadata entirely.

### 1.2 Telegram inbound (`src/channels/telegram.ts`)

The `message:text` handler extracts:

- `ctx.message.message_id` — stored as `id`
- `ctx.message.text` — stored as `content`
- `ctx.from?.id` — stored as `sender`
- `ctx.from?.first_name` — stored as `sender_name`

`ctx.message.reply_to_message` is available in the Grammy context object but
**is never read**. If a user replies to a specific message, the grammY context
carries `ctx.message.reply_to_message` with the full original message object
(including `message_id`, `from`, `text`). This data is silently dropped.

Same applies to media messages via `storeMedia()` — that helper also constructs
a `NewMessage` with no reply fields.

### 1.3 WhatsApp inbound (`src/channels/whatsapp.ts`)

Baileys delivers messages via `messages.upsert`. The code reads:

- `msg.key.id` → stored as `id`
- `msg.key.participant` / `msg.key.remoteJid` → `sender`
- `msg.pushName` → `sender_name`
- `m.conversation || m.extendedTextMessage?.text || ...` → `content`

WhatsApp reply context lives in:

- `m.extendedTextMessage?.contextInfo?.quotedMessage` — the quoted message body
- `m.extendedTextMessage?.contextInfo?.stanzaId` — the id of the message being replied to
- `m.extendedTextMessage?.contextInfo?.participant` — who sent the original

None of these are read. `contextInfo` is completely ignored.

### 1.4 Prompt formatting (`src/router.ts:formatMessages`)

Messages are serialised as:

```xml
<messages>
  <message sender="Alice" time="2026-03-05T10:00:00Z">hello</message>
  <message sender="Bob" time="2026-03-05T10:00:01Z">@Andy help</message>
</messages>
```

There is no `reply_to` attribute, no quoted-text inclusion, no threading
signal at all. The agent sees a flat chronological log.

### 1.5 Agent response threading back

`channel.sendMessage(chatJid, text)` is called in `index.ts` (streaming
callback and IPC handler). Both Telegram and WhatsApp `sendMessage`
implementations send a fresh standalone message with no `reply_to_message_id`
/ no quoted context:

- Telegram: `bot.api.sendMessage(numericId, chunk, { parse_mode: 'HTML' })`
  — no `reply_to_message_id` option passed.
- WhatsApp: `sock.sendMessage(jid, { text: prefixed })`
  — no `quoted` option passed (baileys supports `{ text, quoted: originalMsg }`).

**Result: agent responses are never threaded. They always appear as new
top-level messages in the chat.**

---

## 2. What Is Missing (Gap Analysis)

| Feature                                | Telegram API Support                      | WhatsApp (Baileys) Support           | arizuko Status  |
| -------------------------------------- | ----------------------------------------- | ------------------------------------ | --------------- |
| Capture inbound reply_to               | `ctx.message.reply_to_message.message_id` | `contextInfo.stanzaId`               | Not captured    |
| Quoted text in prompt                  | `ctx.message.reply_to_message.text`       | `contextInfo.quotedMessage`          | Not captured    |
| Reply sender                           | `ctx.message.reply_to_message.from`       | `contextInfo.participant`            | Not captured    |
| Thread response back to triggering msg | `reply_to_message_id` in sendMessage      | `quoted: originalMsg` in sendMessage | Not implemented |
| Store reply_to in DB                   | NewMessage.reply_to field needed          | same                                 | Not in schema   |

---

## 3. How It Could Be Added

### 3.1 `NewMessage` extension

Add an optional field:

```ts
reply_to_id?: string;       // id of the message being replied to
reply_to_content?: string;  // quoted text snippet (for context)
reply_to_sender?: string;   // who sent the original
```

### 3.2 Telegram extraction

In the `message:text` handler:

```ts
const replyTo = ctx.message.reply_to_message;
// replyTo?.message_id, replyTo?.text, replyTo?.from?.first_name
```

### 3.3 WhatsApp extraction (Baileys)

```ts
const ctx = m.extendedTextMessage?.contextInfo;
// ctx?.stanzaId          — id of quoted message
// ctx?.quotedMessage     — WAMessage of the quoted message
// ctx?.participant       — JID of quoted sender
```

### 3.4 Prompt formatting

Add a `reply_to` attribute to the XML format:

```xml
<message sender="Bob" time="..." reply_to="msg-id-123">@Andy, see above</message>
```

Or include a `<quoted>` child element with the original text.

### 3.5 Threading responses back

For Telegram — pass `reply_to_message_id` in sendMessage options:

```ts
bot.api.sendMessage(numericId, text, {
  parse_mode: 'HTML',
  reply_to_message_id: triggeringMsgId,
});
```

For WhatsApp — pass `quoted` to baileys:

```ts
sock.sendMessage(jid, { text }, { quoted: originalWAMessage });
```

This requires storing the triggering WAMessage object, not just the id, because
baileys needs the full key object for the `quoted` parameter.

---

## 4. Related Upstream Note

The OpenClaw project (different upstream, not nanoclaw) had a bug where
`replyToId` vs `replyToMessageId` field name mismatch prevented threading from
working (issue #17880, fixed in PR #17928). That codebase does have
reply-threading as a concept, arizuko does not inherit it from nanoclaw.

---

## 5. Summary

arizuko currently has **no reply threading** on either inbound or outbound side:

- Inbound: `reply_to_message` (Telegram) and `contextInfo.stanzaId` (WhatsApp)
  are available in the raw event objects but are never read or stored.
- Outbound: responses are always sent as standalone messages, never as threaded
  replies to the triggering message.
- The `NewMessage` type has no field for reply context.
- `formatMessages()` generates flat XML with no reply/threading attributes.
- Adding this requires: (a) new fields on `NewMessage`, (b) extraction in both
  channel handlers, (c) prompt format update, and (d) threading in sendMessage.
  WhatsApp threading additionally requires retaining the full WAMessage object
  because baileys `quoted` takes the message object, not just an id.
