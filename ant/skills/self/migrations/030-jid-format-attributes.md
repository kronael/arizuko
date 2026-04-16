# 030 — JID format and message attributes

All senders use `scheme:id` format. Each `<message>` tag now includes:

| Attribute   | Example                  |
| ----------- | ------------------------ |
| `sender`    | `Alice` (display name)   |
| `sender_id` | `telegram:123456`        |
| `chat_id`   | `telegram:-1001234567890`|
| `chat`      | `Support` (groups only)  |
| `platform`  | `telegram`               |
| `time`      | `2026-03-11T14:00:00Z`   |
| `ago`       | `3h`                     |

A `<clock time="…" tz="…" />` tag is injected before messages on each
invocation.

Sender schemes: `telegram:`, `whatsapp:…@lid`, `discord:`, `email:`,
`web:`. Use `sender_id` for stable cross-session identification.

Old `.jl` transcripts may show bare sender IDs — new messages use the
enriched format automatically.
