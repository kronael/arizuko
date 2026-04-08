---
status: shipped
---

# Audit Log

**Status**: shipped

Record every outbound message the gateway sends. Covers agent
replies, MCP actions (send_message, send_reply, send_file),
scheduler output, and control chat notifications. Uses existing
`messages` table with `is_from_me=1` and `is_bot_message=1` —
inbound and outbound share one row shape, distinguished by flags.

Implementation: `store.PutMessage()` in store/messages.go. Called
from gateway (agent output + status messages), ipc (send_message,
send_reply, send_file), and timed scheduler. Unified inbound/outbound
path replaced the earlier `StoreOutbound`/`OutboundEntry` split in v0.25.

## Schema

Migration 0005 added `source TEXT` to `messages`. Migration 0023
dropped the unused `group_folder TEXT` column and repurposed `source`
as the canonical adapter-of-record per message.

`source` values:

- **inbound**: registered adapter name (e.g. `telegram`,
  `telegram-REDACTED`, `discord`). Stamped by `api.handleMessage`
  on every `/v1/messages` delivery.
- **outbound**: empty string (the producer is implied by
  `is_from_me=1`, `is_bot_message=1`, and `sender`).

The producer-category model (agent/mcp/scheduler/control/error)
was abandoned because callers already mark outbound rows via
`is_from_me`/`is_bot_message`, and the column was needed to break
adapter ambiguity for shared JID prefixes.

## API

Outbound messages are written via the unified `PutMessage` path:

```go
store.PutMessage(core.Message{
    ChatJID:   chatJid,
    Content:   text,
    Sender:    groupFolder,
    RoutedTo:  chatJid,
    ReplyToID: parentMsgID,
    FromMe:    true,
    BotMsg:    true,
})
```

Non-blocking: log warning on failure, never propagate error.
ID prefixed `out-` to avoid PK collision with inbound.

## Integration points

| Producer  | File               | What                     |
| --------- | ------------------ | ------------------------ |
| agent     | gateway/gateway.go | streaming agent output   |
| agent     | gateway/gateway.go | delegate/escalate output |
| mcp       | ipc/ipc.go         | send_message tool        |
| mcp       | ipc/ipc.go         | send_file tool           |
| scheduler | timed/main.go      | scheduler messages       |
| control   | gateway/notify.go  | operator notifications   |
| adapter   | api/api.go         | inbound /v1/messages     |

Inbound rows have `source = <adapter-name>`, `is_from_me = 0`.
Outbound rows have `source = ''`, `is_from_me = 1`, `is_bot_message = 1`.

## Queries

```sql
-- Full conversation history (inbound + outbound)
SELECT * FROM messages WHERE chat_jid = ? ORDER BY timestamp;

-- Outbound only
SELECT * FROM messages
WHERE chat_jid = ? AND is_from_me = 1 ORDER BY timestamp;

-- Latest receiving adapter for a chat (used by outbound routing)
SELECT source FROM messages
WHERE chat_jid = ? AND source != '' AND is_bot_message = 0
ORDER BY timestamp DESC LIMIT 1;
```

## Not in scope

- File archiving
- Message delivery confirmation
- Content redaction or retention policies
- Gateway command responses (/ping, /stop — operational noise)
