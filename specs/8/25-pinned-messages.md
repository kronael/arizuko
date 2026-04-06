---
status: planned
phase: next
---

# Pinned Messages as Context

## Problem

Users want to manage persistent agent context directly from the chat.
CLAUDE.md requires file editing; pinned messages are native to
Telegram/Discord and already familiar.

## Concept

Pinned messages in a chat become persistent context for the agent,
similar to CLAUDE.md but user-managed from the platform. Pin a message
to add context; unpin to remove it. The agent sees pins as background
knowledge — not as messages to reply to.

## Platform support

| Platform | Read pins        | Bot can pin | Pin events    |
| -------- | ---------------- | ----------- | ------------- |
| Telegram | getChatPinnedMsg | yes         | PinnedMessage |
| Discord  | channel.Pins()   | yes         | MessageUpdate |
| WhatsApp | no API           | no          | no            |
| Email    | n/a              | n/a         | n/a           |
| Mastodon | pinned statuses  | yes (own)   | n/a           |
| Bluesky  | no API           | no          | no            |
| Reddit   | stickied posts   | yes (mod)   | n/a           |

## Adapter contract

New optional endpoint on adapters:

```
GET /pins?chat_jid=<jid>
→ { "pins": [{ "id": "123", "sender": "alice", "content": "...", "timestamp": 1234567890 }] }
```

Capability: `"read_pins": true` in health response.
Adapters without pin support return `{ "pins": [] }`.

## Gateway integration

### Session start

When building the prompt for a new session (not resumed), gateway
calls `GET /pins` on the channel adapter. Pinned content is injected
as a `<pinned>` XML block before the message history:

```xml
<pinned>
  <pin sender="alice" time="2026-04-05">Always respond in Spanish to @carlos</pin>
  <pin sender="bot" time="2026-04-06">Project deadline: April 15th</pin>
</pinned>
```

### Compaction

PreCompact hook calls `get_pins` MCP tool (which proxies to
`GET /pins` via gated). Injects pins into the compaction
systemMessage with "preserve verbatim" instruction. Pins survive
compaction because they're re-injected, not summarized.

### On-demand

Agent can call `get_pins` MCP tool at any time to re-read current
pins (e.g. after noticing a pin event in the message stream).

## MCP tools

```
get_pins(chatJid) → [{ id, sender, content, timestamp }]
pin_message(chatJid, messageId) → ok  (bot pins a message)
unpin_message(chatJid, messageId) → ok
```

`pin_message` / `unpin_message` require adapter support.
`get_pins` works read-only on any adapter with `read_pins` cap.

## Pin events

Adapters that detect pin/unpin events send them as verb messages:

```json
{ "verb": "pin", "content": "<pinned content>", "sender": "alice" }
{ "verb": "unpin", "content": "", "sender": "alice" }
```

Gateway routes these like normal messages. Agent sees them in
context but the router can filter or annotate them.

## Not in scope

- Storing pins in DB (platform is source of truth)
- Syncing pins across platforms
- Auto-pinning bot messages
- Pin limits or pagination (Telegram: 1 pin, Discord: 50)

## Implementation order

1. `GET /pins` endpoint on teled + discd (read-only)
2. `get_pins` MCP tool in ipc.go
3. Gateway: inject `<pinned>` at session start
4. PreCompact hook: re-inject on compaction
5. Pin/unpin MCP tools + adapter endpoints (later)
6. Pin event verb messages (later)
