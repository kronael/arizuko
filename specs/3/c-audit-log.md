---
status: shipped
---

# Audit log — outbound messages

Every outbound message is recorded in `messages` with `is_from_me=1` and
`is_bot_message=1`. Inbound and outbound share one row shape,
distinguished by flags — not two tables, because "what did this chat
look like" is then one ordered query rather than a merge.

## `source` is the adapter of record

Migration 0005 added `source TEXT`; 0023 dropped the unused
`group_folder` and repurposed `source`:

- **inbound** — the adapter name (`telegram`, `telegram-<suffix>`,
  `discord`), stamped on delivery.
- **outbound** — empty string; the producer is implied by the flags plus
  `sender`.

A producer-category model (`agent`/`mcp`/`scheduler`/`control`/`error`)
was designed and abandoned. Callers already mark outbound via
`is_from_me` / `is_bot_message`, so the category was redundant — and the
column was needed for something else entirely: breaking adapter
ambiguity when two adapters share a JID prefix. That is what the
outbound-routing query uses it for:

```sql
SELECT source FROM messages
WHERE chat_jid = ? AND source != '' AND is_bot_message = 0
ORDER BY timestamp DESC LIMIT 1;
```

## Write discipline

`store.PutMessage` is the single write path. It is non-blocking: a
failure logs a warning and never propagates, because losing an audit row
must not fail the send the user is waiting on. Outbound IDs are prefixed
`out-` to avoid PK collision with inbound.

Per-daemon `audit_log` tables (a separate, later mechanism for
management-action auditing) are covered in
[`../5/17-openapi-mcp.md`](../5/17-openapi-mcp.md); this spec is about
message traffic only.

## Not in scope

File archiving, delivery confirmation, content redaction / retention,
and gateway command responses (`/ping`, `/stop` — operational noise).
