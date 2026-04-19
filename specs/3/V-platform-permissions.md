---
status: unshipped
---

# Platform Permissions

Groups can act on platforms (post, send email) via social actions. No
enforcement today — any group with a platform JID routed to it may call
any action on that platform. Subgroups should not inherit parent's
platform credentials silently.

## Model

A flat `platform_grants` table, parallel to `routes`:

```sql
CREATE TABLE platform_grants (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  folder   TEXT NOT NULL,   -- receives the grant
  platform TEXT NOT NULL,   -- 'twitter', 'email', 'mastodon'
  actions  TEXT NOT NULL    -- JSON array: ["*"] or ["post","reply"]
);
```

Grant lookup at action dispatch: does the calling group have a grant
row for target platform + action? If not, deny.

## Authority

Same as routing:

- Tier 0 — any grant
- Tier 1 — grant to descendants in own world
- Tier 2+ — cannot modify

IPC: `add_platform_grant`, `remove_platform_grant`, `list_platform_grants`.

## Platform resolution

Platform derived from JIDs routed to the group (same as action manifest
filtering). Grant for `twitter` activates only if a `twitter:*` JID
routes to that folder.

## Current behaviour (until implemented)

All groups implicitly granted `["*"]` on all platforms. Action manifest
already filters by platform presence.

## Migration

1. Add `platform_grants` table
2. Seed existing groups with `["*"]` grants for current platforms
3. Enforce at action dispatch in `action-registry.ts`
4. Expose IPC actions

## Open

- Wildcard platform (`*`) in grants
- Inherited grants from parent
- Read vs write distinction
