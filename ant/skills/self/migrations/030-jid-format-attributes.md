# 030 — JID format and message attributes

The gateway now uses `scheme:id` format for all senders and enriches
message XML with additional attributes.

## Message format changes

Each `<message>` tag now includes:

| Attribute   | Example                   | Description              |
| ----------- | ------------------------- | ------------------------ |
| `sender`    | `Alice`                   | Display name             |
| `sender_id` | `telegram:REDACTED`     | Platform-prefixed JID    |
| `chat_id`   | `telegram:-1001234567890` | Chat JID                 |
| `chat`      | `Support`                 | Group name (groups only) |
| `platform`  | `telegram`                | Source platform          |
| `time`      | `2026-03-11T14:00:00Z`    | Message timestamp        |
| `ago`       | `3h`                      | Relative time            |

## Clock header

A `<clock>` tag is injected before messages on each agent invocation:

```xml
<clock time="2026-03-13T10:00:00.000Z" tz="Europe/Prague" />
```

## Sender JID format

All senders now use `platform:id`:

- `telegram:REDACTED`
- `whatsapp:972501234567@s.whatsapp.net` (suffixes preserved)
- `discord:9876543210`
- `email:user@example.com`
- `web:anonymous`

Display names remain in the `sender` attribute. Use `sender_id` for
stable identification across sessions.

## No action required

These changes are gateway-side. Message history in `.jl` transcripts
may show the old format (bare sender IDs). New messages use the
enriched format automatically.
