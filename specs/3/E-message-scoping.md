---
status: in-progress
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
ALTER TABLE routes ADD COLUMN impulse_config TEXT; -- JSON or null
```

Config shape (same as `ImpulseConfig`):

```json
{ "threshold": 100, "weights": { "message": 100 }, "max_hold_ms": 300000 }
```

To suppress triggering on a route, set all weights to 0:

```json
{ "threshold": 100, "weights": { "*": 0 }, "max_hold_ms": 0 }
```

### JID impulse resolution

`getImpulseConfigForJid(jid)` merges configs across all routes for
a JID: if any route has a non-null config, use it; otherwise default.
Platform wildcard routes (e.g. `discord:`) provide the fallback config
when no per-channel config exists.

### Access tiers

| Route     | Saved | Scoped to group | Triggers agent     |
| --------- | ----- | --------------- | ------------------ |
| none      | ✓     | root only       | ✗                  |
| any route | ✓     | group           | per impulse config |

No separate `store` route type. Impulse config with zero weights IS
a store-only route.

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

### Migration

```sql
-- 0NNN-impulse-config.sql
ALTER TABLE routes ADD COLUMN impulse_config TEXT;
```

### DB

```typescript
export function getImpulseConfigForJid(jid: string): ImpulseConfig {
  const platform = jid.split(':')[0] + ':';
  const rows = db
    .prepare(
      'SELECT impulse_config FROM routes WHERE jid IN (?, ?) AND impulse_config IS NOT NULL LIMIT 1',
    )
    .all(jid, platform) as { impulse_config: string }[];
  if (rows.length === 0) return defaultConfig();
  return { ...defaultConfig(), ...JSON.parse(rows[0].impulse_config) };
}
```

### index.ts

Remove `isSocialJid()` check and global `impulseConfig`. Apply
per-JID config to every message batch:

```typescript
// before: if (isSocialJid(chatJid)) { accumulate with global config }
// after: always accumulate with per-JID config
const cfg = getImpulseConfigForJid(chatJid);
let iState = impulseStates.get(chatJid) ?? emptyState();
// ... accumulate, check flush
```

### Platform wildcard example

```sql
-- Store all Discord in atlas/content scope, never trigger
INSERT INTO routes (jid, type, seq, target, impulse_config)
VALUES ('discord:', 'default', 9999, 'atlas/content',
        '{"threshold":100,"weights":{"*":0},"max_hold_ms":0}');
```

## MCP enforcement location

Message query actions (`get_messages`, `read_messages`):

1. Resolve caller folder from container context
2. Check `routes.target` includes caller folder for requested JID
3. If not: return `access_denied` error
4. Root: bypass check
