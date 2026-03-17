# Audit Log

**Status**: design

Record every outbound message the gateway sends. Covers agent
replies, MCP actions (send_message, send_reply, send_file),
scheduler output, and control chat notifications. Uses existing
`messages` table with `is_from_me=1` — same structure, filtered
by existing queries (`is_bot_message=0`).

## Schema

Migration adds columns to `messages`:

```sql
ALTER TABLE messages ADD COLUMN source TEXT;
ALTER TABLE messages ADD COLUMN group_folder TEXT;
```

`source` values: `agent`, `mcp`, `scheduler`, `control`, `error`.

## API

```go
// store/outbound.go
func (s *Store) StoreOutbound(entry OutboundEntry) error

type OutboundEntry struct {
    ChatJID      string
    Content      string
    Source       string
    GroupFolder  string
    ReplyToID    string
    PlatformMsgID string
}
```

Non-blocking: log warning on failure, never propagate error.
ID prefixed `out-` to avoid PK collision with inbound.

## Integration points

| Source    | File               | What                     |
| --------- | ------------------ | ------------------------ |
| agent     | gateway/gateway.go | streaming agent output   |
| agent     | gateway/gateway.go | delegate/escalate output |
| mcp       | icmcd/icmcd.go     | send_message tool        |
| mcp       | icmcd/icmcd.go     | send_file tool           |
| scheduler | timed/main.go      | scheduler messages       |
| control   | gateway/notify.go  | operator notifications   |

## Queries

```sql
-- Full conversation history (inbound + outbound)
SELECT * FROM messages WHERE chat_jid = ? ORDER BY timestamp;

-- Outbound only
SELECT * FROM messages
WHERE chat_jid = ? AND is_from_me = 1 ORDER BY timestamp;

-- Outbound by source
SELECT * FROM messages
WHERE source = 'agent' AND timestamp > datetime('now', '-1 day');
```

## Not in scope

- File archiving
- Message delivery confirmation
- Content redaction or retention policies
- Gateway command responses (/ping, /stop — operational noise)
