---
status: shipped
---

# Audit Log

Every outbound message the gateway sends is recorded. Covers agent
replies, MCP actions (send_message, send_reply, send_file), scheduler
output, and control chat notifications. Reuses `messages` table with
`is_from_me=1` and `is_bot_message=1` — inbound and outbound share one
row shape, distinguished by flags.

## Schema

Migration 0005 added `source TEXT` to `messages`. Migration 0023 dropped
unused `group_folder` and repurposed `source` as canonical
adapter-of-record per message.

`source` values:

- **inbound**: adapter name (`telegram`, `telegram-<suffix>`, `discord`).
  Stamped by `api.handleMessage` on `/v1/messages` delivery.
- **outbound**: empty string (producer implied by flags + sender).

Producer-category model (agent/mcp/scheduler/control/error) was
abandoned — callers already mark outbound via `is_from_me`/`is_bot_message`,
and the column was needed to break adapter ambiguity for shared JID
prefixes.

## API

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

Non-blocking: log warning on failure, never propagate. ID prefixed `out-`
to avoid PK collision with inbound.

## Producers

| Producer  | File               | What                     |
| --------- | ------------------ | ------------------------ |
| agent     | gateway/gateway.go | streaming agent output   |
| agent     | gateway/gateway.go | delegate/escalate output |
| mcp       | ipc/ipc.go         | send_message tool        |
| mcp       | ipc/ipc.go         | send_file tool           |
| scheduler | timed/main.go      | scheduler messages       |
| control   | gateway/notify.go  | operator notifications   |
| adapter   | api/api.go         | inbound /v1/messages     |

Inbound: `source = <adapter>`, `is_from_me = 0`.
Outbound: `source = ''`, `is_from_me = 1`, `is_bot_message = 1`.

## Queries

```sql
-- Full conversation
SELECT * FROM messages WHERE chat_jid = ? ORDER BY timestamp;

-- Outbound only
SELECT * FROM messages
WHERE chat_jid = ? AND is_from_me = 1 ORDER BY timestamp;

-- Latest receiving adapter (used by outbound routing)
SELECT source FROM messages
WHERE chat_jid = ? AND source != '' AND is_bot_message = 0
ORDER BY timestamp DESC LIMIT 1;
```

## Not in scope

- File archiving
- Message delivery confirmation
- Content redaction / retention policies
- Gateway command responses (/ping, /stop — operational noise)
