---
status: draft
depends: [E-routd, Q-unified-routing]
---

# Final-delivery outbox

When a turn finishes, its reply must reach the channel **exactly once,
eventually**, even if the adapter or routd restarts mid-delivery.
arizuko's outbound is poll-based (`Q-unified-routing`): routd appends
the assistant `messages` row, the adapter polls and sends. If the
adapter is down or the send fails, there is no record that _this
terminal reply still owes delivery_ — recovery relies on the row still
being unsent, with no retry accounting and no "delivered" acknowledgement
distinct from "appended".

## What we steal

Centaur's `agent_final_delivery_outbox` table and the API's
"final-delivery recovery" obligation: the terminal answer is an
outbox entry retried until the channel confirms, surviving service
restarts. "The API owns ... final-delivery recovery; Postgres is the
source of truth."

## arizuko-shaped design

routd owns the message store and is the sole appender (`5/E`). The
terminal assistant frame already lands as a `messages` row; this adds a
delivery-state column set so retry is explicit and idempotent, rather
than a new table that duplicates the message.

- On the terminal assistant frame, routd marks it `delivery=pending`.
- The adapter's existing poll → send path acks back to routd
  (`POST /v1/turns/{turn_id}/delivered` or a column write via routd's
  own `/v1/*`): `delivery=delivered` with a `delivered_at`.
- A routd background tick (sibling to the existing `pollOnce`) re-offers
  `pending` terminal frames older than `DELIVERY_RETRY_AFTER` (default
  30s), bounded by `DELIVERY_MAX_ATTEMPTS`; exhausted → `delivery=failed`
  - an `audit_log` row, no infinite loop.
- Idempotency: the adapter dedups on `messages.id`; redelivery of an
  already-sent frame is a no-op send guarded by the platform message id
  routd already records.

Only terminal frames enter the retry discipline — intermediate progress
frames stay best-effort (they are observability, not the result). This
keeps the outbox small and matches Centaur's "final delivery" scope.

## Out of scope

- Streaming/progress delivery guarantees — `15/1` handles reconnect for
  observers; intermediate frames are not retried.
- Cross-channel fan-out of one reply — one terminal frame, one target
  chat.
- Exactly-once across a platform that itself drops acks — best-effort
  ack from the adapter is the boundary; platform-side dedup is the
  adapter's concern.
