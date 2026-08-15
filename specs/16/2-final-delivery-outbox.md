---
status: shipped
absorbed-into: [5/Q-unified-routing, 5/E-routd]
---

# Final-delivery outbox

Absorbed — the guarantee ships under existing names, not as a new table.
This file stays as the record of where each piece landed; the living
description is [`5/Q`](../5/Q-unified-routing.md) § "Outbound is
poll-reconciled" and [`5/E`](../5/E-routd.md) § "Dedup is the adapter's
contract".

The Centaur-inspired outbox obligation — the terminal reply is retried
until the channel confirms, surviving restarts — maps onto arizuko as:

- Outbox entry = the bot `messages` row written `status='pending'`
  (routd is the sole appender, `5/E`). No second table.
- Re-offer tick = `maybeRetryOutbound`, per-row exponential backoff,
  bounded by `MAX_OUTBOUND_RETRY`, exhausted → `failed` (the
  dead-letter state, `5/Q`).
- Delivered-ack = the adapter's 200 on `/send`; `MarkBotPlatformID`
  stamps `platform_id` + `sent` in one statement (`5/34`).
- Idempotent redelivery = the row's stable id sent as
  `idempotency_key`, claimed/settled in the adapter's own durable
  ledger (`chanlib/dedup.go`, `5/E`).

Scope survived unchanged: only terminal frames enter the retry
discipline (progress frames are observability, `16/1`); one terminal
frame, one target chat; platform-side dedup is the adapter's concern.
