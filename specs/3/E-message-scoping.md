---
status: shipped
---

# Message Scoping + Impulse Gate

## Problem

1. Routing and triggering were conflated — a route fires the agent.
   No way to store messages in a group's scope without triggering.
2. Impulse was applied only to social JIDs (`isSocialJid`), not
   universally. Channel type controlled trigger timing.
3. Non-routed messages were inaccessible except to root.

## Design

### Impulse = universal trigger gate

All messages go through impulse before agent dispatch. Impulse owns
trigger decisions; routing owns scope only.

```
Channel → DB (always)
        → impulse (per-JID config) → if fires → routing → agent
```

`isSocialJid()` deleted. No channel-type logic in trigger path.

### Per-route impulse config

Each `routes` row has optional `impulse_config` JSON blob. Null =
default (threshold=100, message=100 → fires on every message).

```json
{ "threshold": 100, "weights": { "message": 100 }, "max_hold_ms": 300000 }
```

Suppress by zeroing weights:

```json
{ "threshold": 100, "weights": { "*": 0 }, "max_hold_ms": 0 }
```

### Resolution

`GetImpulseConfigJSON(msg)` walks routes in `seq` order, returns the
first non-null `impulse_config` whose `match` matches the message,
else default. Platform wildcard routes (`match=platform=discord`)
provide the fallback.

### Access tiers

| Route     | Saved | Scoped to group | Triggers agent     |
| --------- | ----- | --------------- | ------------------ |
| none      | ✓     | root only       | ✗                  |
| any route | ✓     | group           | per impulse config |

No separate "store-only" route kind — a zero-weight route is store-only.

### Access control: DENY not filter

When an agent queries messages via MCP/IPC, out-of-scope JIDs return a
tool error, not empty list:

```json
{
  "error": "access_denied",
  "message": "You can only query messages routed to your group (atlas/content). Request a root operator to grant cross-group access."
}
```

Root (tier 0) bypasses.

## Files

- `store/` — `GetImpulseConfigJSON` walks routes in seq order
- `gateway/gateway.go` — apply per-message config per batch
- `ipc/` — `get_messages`/`read_messages` resolve caller folder, check
  that some matching route's target is at/under that folder; root bypasses
