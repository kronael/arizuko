---
status: draft
phase: next
---

# ACL: no rows = no access

Flip `user_groups` semantics. Today: no rows = operator (unrestricted).
After: no rows = no access. `*` row = operator.

## Changes

- `store/auth.go`: `UserGroups` returns empty list when no rows
  (currently returns nil = operator). Add `*` check: if any row
  has folder `*`, return nil (operator).
- No existing users — safe to flip without migration.

## Enforcement points

- `proxyd`: already sets `X-User-Groups` from JWT claims. No change.
- `webd`: `requireFolder` already checks `X-User-Groups`. No change.
- Route creation (onbod, ipc): check `user_groups` before inserting.
  Currently unchecked — add guard.
- Gateway message routing: does NOT enforce `user_groups`. Messages
  route by `routes` table match, not by user identity. This is
  correct — the route was authorized at creation time.

## Operator bootstrap

No special flow. Operator inserts `user_groups` row with folder `*`
via SQL or CLI. First deploy seeds this in the migration.
