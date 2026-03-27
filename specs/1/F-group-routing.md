---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Gateway Routing

One flat routing table. Messages arrive on a JID, router scans,
resolves a destination folder.

## Routing table

```sql
CREATE TABLE routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  jid TEXT NOT NULL,
  seq INTEGER NOT NULL,      -- lower first
  type TEXT NOT NULL,        -- command/verb/pattern/keyword/sender/default
  match TEXT,
  target TEXT NOT NULL       -- destination folder
);
```

Evaluation: select routes for JID ordered by `seq`, first match wins.

Rule types: `command` (prefix), `verb` (equals), `pattern` (regex),
`keyword` (case-insensitive substring), `sender` (regex on JID),
`default` (catch-all).

## Authorization boundaries

Root world groups (`root`, `root/*`) can delegate anywhere.
Others can only delegate to descendants in the same world.

IPC routing actions: tier 0 modifies any routes, tier 1 own
subtree only, tier 2+ cannot modify routes.

## Error handling

- **No route match**: message stored but not processed, debug log
- **Delegation failure**: cursor advances (marked processed),
  error logged, no retry; message still in DB for parent access
- **Authorization**: checked at runtime during route resolution (`IsAuthorizedRoutingTarget`)

## Open questions

- Strip command prefix before child sees it?
- Wildcard JID routes (`*`) for catch-all across all chats?
- Broadcast mode: route to multiple targets?
