---
status: draft
phase: next
---

# Pinned messages as context

Users want to manage persistent agent context from the chat itself. Editing
`CLAUDE.md` needs file access; pinning a message is native to Telegram and
Discord and already familiar. A pin becomes background knowledge for the
agent — not a message to reply to.

## Platform support

The reason this is worth specifying: support is uneven, so the contract has
to degrade cleanly rather than assume the feature exists.

| Platform | Read pins        | Bot can pin | Pin events    |
| -------- | ---------------- | ----------- | ------------- |
| Telegram | getChatPinnedMsg | yes         | PinnedMessage |
| Discord  | channel.Pins()   | yes         | MessageUpdate |
| Mastodon | pinned statuses  | yes (own)   | no            |
| Reddit   | stickied posts   | yes (mod)   | no            |
| WhatsApp | no API           | no          | no            |
| Bluesky  | no API           | no          | no            |
| Email    | n/a              | n/a         | n/a           |

## Decisions

- **The platform is the source of truth; arizuko stores nothing.** Pins are
  read from the adapter, never mirrored into a table. A second copy would
  need reconciliation, and the operator can already see the truth in the
  chat.
- **Optional adapter capability, not a required endpoint.** Adapters expose
  `GET /pins?chat_jid=…` and advertise `read_pins` in their health response;
  adapters without pin support return an empty list rather than an error.
  Write tools (`pin_message` / `unpin_message`) require explicit support;
  reading works on any adapter that advertises the capability.
- **Pins are re-injected, not summarized.** They enter the prompt as a
  `<pinned>` block at session start, and the compaction hook re-injects them
  rather than letting them be compacted away. That is what makes a pin
  durable — surviving compaction is the whole point of pinning.
- **Pin/unpin events arrive as ordinary verb messages**, routed like any
  other message, so the agent can notice a pin changed and re-read.

## Not in scope

- Storing pins in the DB (the platform is truth).
- Syncing pins across platforms.
- Auto-pinning the agent's own messages.
- Pin limits and pagination (Telegram allows one, Discord fifty).
