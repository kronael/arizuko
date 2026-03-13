---
status: spec
---

# Platform Permissions

Groups can act on platforms (post to Twitter, send email, etc.) via
social actions. There is no enforcement today — any group that has a
platform JID routed to it can call any action on that platform.

This spec defines a permissions layer for platform actions, modeled
after the routing table: explicit rows, same authority rules.

## Problem

A subgroup should not automatically inherit its parent's platform
credentials. The routing layer controls _which messages reach a group_;
a platform permissions layer controls _which actions a group may
perform on a platform_.

Example: `atlas` has a twitter JID routed to it. `atlas/support` should
not be able to post tweets unless explicitly granted.

## Proposed model

A flat `platform_grants` table, parallel to `routes`:

```sql
CREATE TABLE platform_grants (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  folder  TEXT NOT NULL,   -- group folder that receives the grant
  platform TEXT NOT NULL,  -- e.g. 'twitter', 'email', 'mastodon'
  actions TEXT NOT NULL    -- JSON array: ["*"] or ["post","reply"]
);
```

Grant lookup at action dispatch time: does the calling group's folder
have a grant row for the target platform + action? If not, deny.

## Authority

Same rules as routing:

- Tier 0 (root) — can create/delete any grant
- Tier 1 (world root, e.g. `atlas`) — can grant to descendants in own
  world only
- Tier 2+ — cannot modify grants

IPC actions: `add_platform_grant`, `remove_platform_grant`,
`list_platform_grants`.

## Platform resolution

Platform is derived from the JIDs that route to the group
(same as action manifest filtering today). A grant for `twitter`
only activates if a `twitter:*` JID routes to that folder.

## Current behavior (until implemented)

All groups are implicitly granted `["*"]` on all platforms.
The action manifest already filters by platform presence — a group
without a twitter JID routed to it won't see twitter actions regardless
of grants.

## Migration path

1. Add `platform_grants` table (migration)
2. Seed existing groups with `["*"]` grants for their current platforms
3. Enforce at action dispatch in `action-registry.ts`
4. Expose IPC actions for grant management

## Open

- Wildcard platform (`*`) in grants — allow all platforms?
- Inherited grants — child inherits parent's grants unless explicitly
  restricted?
- Read vs write distinction — some platforms may allow read-only by
  default, write requires explicit grant
