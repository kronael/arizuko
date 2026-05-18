---
status: draft
---

# whapd auth-dir auto-rotate on 401 storms

Sibling to `specs/8/15-whapd-self-rebind.md`. Independent concern,
deliberately separate spec.

## Problem

When WhatsApp invalidates a session server-side, whapd loops
`401 session invalidated, delete auth dir and re-pair` indefinitely
— Baileys' default reconnect backoff is bounded but the loop never
gives up. Outcome: CPU churn, journal pollution, and the operator
must SSH in to recover (covered by spec 8/15 for the recovery side).

## What ships

After **N consecutive 401s within W seconds**, whapd:

1. Acquires `WhapdBot.pairing.mu` (shared with spec 8/15's pair flow).
   If state != `idle`, skip rotation this cycle — pairing in flight,
   rotation would race the handshake.
2. Stops the reconnect loop (sets `suspended = true`).
3. Moves the auth dir aside (`whatsapp-auth/` →
   `whatsapp-auth.bak.<unix-ts>/`), recreates an empty
   `whatsapp-auth/`.
4. Sets internal state to `unauthenticated`.
5. Writes an audit row to `messages` (same shape as 8/15, see that
   spec's "Audit" section — synthetic JID `arizuko:admin/whapd`,
   `sender='whapd:auth-rotate'`, `verb='admin.auth-rotate'`,
   `content='rotated after N×401 in W seconds'`).
6. Releases the mutex.

That state is what `GET /v1/pair/status` reports (per spec 8/15).
The operator sees "unauthenticated" in dashd and clicks Start.

## Why a separate spec

The dashd re-pair flow (8/15) works whether or not auth-rotate ships
— operator can just wait through the 401 loop, click pair, done. The
rotation is a hygiene fix (kills the CPU loop) that's nice but not
required for the recovery story.

Bundling would braid two concerns: (a) UI for operator-initiated
recovery, (b) automatic self-cleanup of the failure state. Per the
"one fix touches exactly one concern" rule, they ship as two
commits, possibly in different sprints.

## Tunables

```
WHAPD_AUTH_ROTATE_THRESHOLD=3       # consecutive 401s
WHAPD_AUTH_ROTATE_WINDOW_SEC=60     # within this window
WHAPD_AUTH_ROTATE_ENABLED=true      # kill switch
```

Defaults above. Threshold is conservative — Baileys' backoff
(3s, 6s, 12s, 24s, 48s) means 3 fast 401s only happen on genuine
session-revoke; transient outages spread further apart and don't
trip.

## Open questions

1. ~~`messages` vs `channel_events` table?~~ Decided jointly with
   8/15: stay in `messages` with synthetic JID `arizuko:admin/whapd`.
   No new schema; both specs adopt the same convention.
2. Should the rotation hand off to a follow-up "operator notification"
   (DM the operator group with "WhatsApp needs re-pair, /dash/
   channels/whatsapp/pair")? Probably yes; tracks separately.
3. How are the `whatsapp-auth.bak.<ts>/` dirs pruned? Manual today,
   or a TTL on whapd boot?

## What this is NOT

- NOT the re-pair UI. See spec 8/15.
- NOT a way to recover sessions that 401-blip transiently. The
  threshold is intentionally high enough to not trip on noise.
- NOT a generic adapter pattern. Same scoping caveat as 8/15 — one
  adapter today; abstract when a second one needs it.
