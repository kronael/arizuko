---
status: shipped
---

# Message Scoping + Impulse Gate

## Problem

1. Routing and triggering are conflated — a route fires the agent.
   There is no way to store messages in a group's scope without
   triggering an agent run.
2. Impulse is applied only to social JIDs (`isSocialJid()`),
   not universally. Channel type controls trigger timing.
3. Non-routed messages are inaccessible except to root.

## Design

### Impulse is the universal trigger gate

All messages — every JID, every platform — go through impulse
before reaching the agent dispatch loop. Impulse owns all trigger
decisions. Routing owns scope/destination only.

```
Channel → DB (store, always)
        → impulse (per-JID config) → if fires → routing → agent
```

`isSocialJid()` is deleted. No channel-type logic in the trigger path.

### Per-route impulse config

Each route row carries an optional impulse config (JSON blob).
If null, the default config applies (threshold=100, message=100 →
fires on every message, current behavior).

```sql
-- Column exists in the current routes schema:
-- routes(id, seq, match, target, impulse_config)
```

Config shape (same as `ImpulseConfig`):

```json
{ "threshold": 100, "weights": { "message": 100 }, "max_hold_ms": 300000 }
```

To suppress triggering on a route, set all weights to 0:

```json
{ "threshold": 100, "weights": { "*": 0 }, "max_hold_ms": 0 }
```

### Message impulse resolution

`GetImpulseConfigJSON(msg)` walks the routes table in `seq` order,
returns the first non-null `impulse_config` whose `match` expression
matches the current message, otherwise the default. Platform
wildcard routes (e.g. `match=platform=discord`) provide the fallback
config when no per-room config exists.

### Access tiers

| Route     | Saved | Scoped to group | Triggers agent     |
| --------- | ----- | --------------- | ------------------ |
| none      | ✓     | root only       | ✗                  |
| any route | ✓     | group           | per impulse config |

No separate "store-only" route kind. An impulse config with zero
weights on an otherwise normal route row IS a store-only route.

### Access control: DENY not filter

When an agent queries messages via MCP/IPC, scope is enforced
strictly — not by silently filtering results but by denying outright
if the requested JID is outside the agent's group scope.

**Violation response** (tool error, not empty list):

```json
{
  "error": "access_denied",
  "message": "You can only query messages routed to your group (atlas/content). Request a root operator to grant cross-group access."
}
```

Root (tier 0) bypasses scope check — sees all JIDs.

## Implementation

### DB

```go
// GetImpulseConfigJSON walks routes in seq order, returns the first
// non-null impulse_config whose match evaluates true for msg.
func (s *Store) GetImpulseConfigJSON(msg core.Message) string
```

### Gateway

Apply per-message config to every batch:

```go
cfgJSON := store.GetImpulseConfigJSON(msg)
// ... accumulate, check flush
```

### Platform wildcard example

```sql
-- Store all Discord in atlas/content scope, never trigger
INSERT INTO routes (seq, match, target, impulse_config)
VALUES (9999, 'platform=discord', 'atlas/content',
        '{"threshold":100,"weights":{"*":0},"max_hold_ms":0}');
```

## MCP enforcement location

Message query actions (`get_messages`, `read_messages`):

1. Resolve caller folder from container context
2. Check that some route whose match evaluates true for the requested
   JID has `target` equal to (or under) the caller folder
3. If not: return `access_denied` error
4. Root: bypass check
