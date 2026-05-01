---
status: superseded
superseded_by: GRANTS.md
---

# Platform Permissions

Superseded by [`GRANTS.md`](../../GRANTS.md).
The proposed `platform_grants` table was abandoned in favor of
routes-derived permissions: a folder's ability to act on a platform
is computed from the `routes` rows that target it (the "RoutedJIDs +
platformRules" composition), not stored as a separate ACL table.
Action manifest filtering already gates platform actions on JID
presence; that composition replaced the dedicated grants table.
