---
status: shipped
shipped: 2026-05-01
---

# Unified message routing

Single message table, single router decision point, uniform
`prefix:identifier` addressing.

Principles:

- All messages (user input + agent output) flow through DB.
- Router is sole decision point; no direct enqueue/callback shortcuts.
- Uniform addressing: channels = `platform:account/id`, groups =
  bare folder path (no prefix). `:` in target distinguishes typed
  destinations (`daemon:`, `builtin:`, `folder:`) from bare folder
  paths.

Router priority on resolution: @mention (explicit) > reply-chain
(implicit continuation) > sticky (session) > default.
Agent messages (sender without `:`) never get content-based routing.

## What shipped (v0.25 → 2026-05-01)

- Agent outputs written via `PutMessage`; delegation/escalation as
  messages with `forwarded_from`. `EnqueueTask`/`OutboundEntry`
  removed. MCP tools write messages directly.
- `local:` prefix dropped. Inter-group / system messages use the bare
  folder path (`atlas/content`, not `local:atlas/content`).
  `LocalChannel.Owns` claims any JID without `:` that matches a
  registered group; real channel JIDs always carry `platform:`.
- `messages.status` column (migration 0038): `'sent'` (default /
  inbound / suppressed), `'pending'` (outbound queued), `'failed'`
  (terminal). `MarkMessageDelivered`/`MarkMessageStatus`/
  `PendingOutboundOlderThan` form the poll-based delivery API.
- Outbound delivery is poll-driven: gateway writes the bot row as
  `pending`, attempts delivery in line, marks `sent` on success.
  An `outboundRetryLoop` scans `pending` rows older than 30s and
  retries; rows older than 24h are marked `failed` and stopped.
  The in-memory `chanreg` outbox stays for adapter reconnect drains.
- `send_message` and `send_file` MCP tools stay distinct
  (different intents, different sharp descriptions per the project's
  tool-naming rule). Behind the MCP wall they funnel through one
  shared internal `internalSend(jid, text, files)` helper so
  persistence (`recordOutbound`) and routing remain symmetric. Prior
  asymmetry — `send_file` did not record outbound rows — fixed by
  this consolidation.
